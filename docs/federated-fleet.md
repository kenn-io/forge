# Federated fleet

Middleman daemons federate into a fleet: one daemon (the **hub**) merges
its own state with the state of remote daemons (**peers**) into a single
snapshot, proxies mutations to the peer that owns the resource, and tells
clients how to attach native terminals to sessions anywhere in the fleet.
Every daemon runs the same binary; hub vs. peer is purely a matter of
which daemon a client talks to and which peers its config lists.

This document is the single reference for the fleet's scope, wire
contracts, and transports. It supersedes the per-slice design documents
that previously lived under `docs/superpowers/`.

## Scope

What the fleet does today:

- **Read plane** — the hub fans out to every configured peer, fetches
  each peer's raw snapshot, and merges them into one enriched snapshot
  (projects, worktrees, sessions, hosts, live tmux state).
- **Write plane** — mutations against a remote host's resources
  (workspace create/delete, runtime session launch/stop, registered-
  worktree runtime, filesystem probes) ride hub-keyed proxy routes.
- **Terminal plane** — attach-spec reads return the native command
  vector for a tmux-backed session; WebSocket terminal routes proxy
  interactive sessions through the hub.
- **Transports** — peers are reached over plain HTTP or over SSH
  (the hub holds a ControlMaster per SSH peer and relays API calls by
  executing the peer's own CLI remotely, so the peer's HTTP listener
  never leaves its host).
- **Daemon contract** — runtime discovery via the lock file and
  published metadata, a minted bearer token, and the `middleman api`
  CLI verb give thin clients and relays one uniform way to reach a
  daemon.

Out of scope (deliberately): remote binary provisioning/bootstrap
(peers are expected to have `middleman` installed), cross-fleet
identity beyond config keys, and any peer-to-peer topology — fan-out is
always hub → peer, one hop.

## Configuration

```toml
[fleet]
# Fleet federation is opt-in. Set this on hubs that should fetch and
# proxy remote peers; leaving it false keeps remote hosts unavailable.
enabled = true
# This daemon's identity in the fleet. Required to be unique across
# the fleet; empty means "anonymous member" (can be a peer, cannot
# meaningfully host).
key = "studio"
# Per-peer snapshot fetch budget (default "2s"). A peer that does not
# answer in time degrades (reachable=false) instead of stalling the
# snapshot.
peer_timeout = "2s"

[fleet.sessions]
# Unmanaged tmux sessions are redacted to summary-only (name + window
# count) unless the operator opts in to full window details.
include_unmanaged_details = false

# HTTP peer: the hub fetches http://mini.tail:8091/api/v1/snapshot/raw
# and proxies writes to the same base URL. HTTP peers are credential-free
# from the hub: the hub sends no token and never forwards the caller's
# Authorization/cookie (those authenticate the hub, not the peer). So the
# peer daemon must NOT set [api].require_auth, and base_url must ride a
# trusted transport boundary (loopback, tailnet, or a VPN). Use an SSH
# peer when the target needs authentication or has no exposed listener.
[[fleet.peers]]
key = "mini"
name = "Mac mini"
base_url = "http://mini.tail:8091"

# SSH peer: no exposed listener. The hub opens a ControlMaster to the
# destination and relays API calls by executing the remote CLI.
[[fleet.ssh_peers]]
key = "epyc"
name = "EPYC box"
destination = "wes@epyc.tail"
# platform = "linux"
# remote_command = "middleman"   # bare executable, no flags

[api]
# Gate this daemon's own HTTP API and terminal WebSocket routes behind
# the minted bearer token (see "Daemon contract"). Recommended whenever
# browsers or SSH-relay peers reach this daemon. Do not enable it on a
# daemon that other hubs reach as an HTTP peer: HTTP peer fetches are
# credential-free and would be rejected.
require_auth = true
```

Validation rules (`internal/config/config.go`): peer keys must be
non-empty and unique across `fleet.key`, `[[fleet.peers]]`, and
`[[fleet.ssh_peers]]`; SSH `destination` is required (it is passed to
ssh(1) as a single positional argument, never through a shell);
`remote_command` must be a bare executable name or path — the relay
embeds it unquoted in a remote shell fragment and the CLI only
dispatches subcommands in argv[0] position, so flags or metacharacters
would change meaning. A custom remote config location is set via
`MIDDLEMAN_HOME` in the remote login profile (the relay runs
`sh -lc`). Editing `[api].require_auth` or the SSH peer set while the
daemon runs reports `restart_required` on the `config.changed` event —
both are wired at startup.

## Read plane: snapshots

Two snapshot endpoints with distinct roles:

- `GET /api/v1/snapshot/raw` — this host only, never fans out. The
  schema-versioned wire shape (`fleet.RawSnapshot`, `schemaVersion: 2`,
  `internal/fleet/types.go`) carries the host record (hostname, platform,
  version, live tmux inventory), projects, worktrees, and sessions,
  each tagged with a `scopedKey` that is unique fleet-wide.
- `GET /api/v1/snapshot?include_peers=true` — the merged, enriched
  view. The hub fetches every configured peer's `/snapshot/raw`
  concurrently (`fleet_hub.go`), bounded per peer by
  `fleet.peer_timeout`, and merges results (`fleet.Merge`,
  `fleet.BuildEnriched`). Without `include_peers` the hub answers from
  local state alone.

Peer failures degrade rather than break the merge: an unreachable or
slow peer appears in `hosts[]` with `reachable: false` and a
diagnostic `error`, while the rest of the snapshot stays intact. For
SSH peers a cold connection returns a "connection warming" degraded
result while the fetch continues in the background (single-flighted
per peer), so the next read benefits.

Enrichment (`internal/fleet/enrich.go`) computes two per-host layers:

- **Diagnostics** — informational warnings (tmux missing, platform
  auth absent, probe errors) surfaced to the UI.
- **Operation availability** — a binary gate per operation. The hub
  applies a routability policy (`fleet_hub.go`): operations it cannot
  route to a host are suppressed with an explanatory reason. HTTP and
  SSH peers are routable (proxy routes exist), so their workspace and
  session operations stay available; repo/project-registry mutations
  remain local to each host.

A background monitor (`fleet_platform_auth.go`) keeps the local host's
platform-authentication signal fresh (resolving a token can shell out
to `gh auth token`, so it never runs on the snapshot read path).

## Live tmux monitoring

Each daemon monitors its own tmux server and reports the result in its
raw snapshot (`fleet_tmux_monitor.go`, `fleet_tmux_reconcile.go`):

- **Inventory probe** every 4s (`tmux list-sessions` /
  `list-windows`, 750ms timeout): names, windows, created-at.
- **Metrics probe** every 15s, best-effort: per-session CPU, RSS,
  process count.
- **Reconciliation** joins live sessions against DB ownership rows
  (workspace sessions and registered-worktree sessions). A live owned
  session is `managed: true` and carries its `sessionScopedKey` join
  key; a live unowned session is `managed: false` and redacted to
  summary unless `include_unmanaged_details` is set. An owned session
  absent from two consecutive samples (both newer than the ownership
  row) is marked exited — the two-sample debounce absorbs the race
  with session creation.
- Probe failures surface as `tmuxProbeError` / `tmuxMetricsError` on
  the host record with `tmuxLastPolledAt` for staleness.

## Write plane: fleet proxy routes

Hub-keyed routes under `/api/v1/fleet/hosts/{host_key}/...`
(`fleet_proxy.go`, `fleet_project_proxy.go`) cover:

- workspace create (including from an issue) and delete
- workspace runtime sessions: launch, stop, attach-spec
- host-level runtime sessions: launch, stop, attach-spec
- registered-worktree runtime: info, ensure-shell, launch/stop
  session, attach-spec
- filesystem completion and repo validation (for create-workspace UIs)

Resolution order for `{host_key}`: the local host serves itself; an
HTTP peer gets a reverse proxy to its base URL; an SSH peer gets the
CLI relay (below). The request body rides verbatim and the remote's
exact status code and body come back — error bodies are RFC 9457
problem documents the caller wants.

WebSocket terminal routes (`/ws/v1/fleet/hosts/{host_key}/...`) proxy
interactive terminals through the hub for workspace, runtime-shell,
and runtime-session targets.

## Terminal plane: attach-specs

`GET .../attach-spec` returns the native command vector for a
tmux-backed session:

```json
{
  "version": 1,
  "kind": "tmux",
  "session_key": "...",
  "target_key": "...",
  "tmux_session": "mm-...",
  "command": ["tmux", "attach-session", "-t", "mm-..."],
  "requires_local_host": true
}
```

The session must exist both as a DB ownership row and in live tmux;
non-tmux sessions are not attachable. When the fleet proxy serves an
attach-spec for an **SSH peer**, it wraps the command so it runs from
the hub's host through the peer's ControlMaster —
`ssh -o ControlPath=<socket> -o ControlMaster=no -t <destination>
tmux attach-session ...` — and flips `requires_local_host` to false
(`fleet_ssh.go: wrapAttachSpecForSSH`). Clients consume the socket
path and connection state; they never spawn their own masters.

## SSH transport

`internal/sshfleet` owns the SSH side; the hub is the single owner of
the ControlMaster lifecycle.

- **ConnectionManager** (`connection.go`) — one master per peer
  (`-MNf`, `ServerAliveInterval=15`, `BatchMode=yes`,
  `ConnectTimeout=10`), deterministic socket path under
  `<data_dir>/ssh-sockets` (short hash of the host key). Sockets that
  survive a daemon restart are adopted when still alive; per-host
  connect coalescing prevents racing spawns; an idle monitor reaps
  masters after 30 minutes without activity. State transitions
  (connecting / connected / probe_failed / disconnected / error)
  broadcast on the event hub as `fleet_host_state` and map onto the
  snapshot's `connectionState`.
- **Runner** (`runner.go`) — relays one HTTP exchange by executing
  the remote CLI through the master:
  `ssh -o ControlPath=<socket> -o ControlMaster=no <destination>
  sh -lc '<PATH=...>; middleman api -i [-d @-] METHOD PATH'`. The
  `-i` framing (status line, blank line, body) lets the hub recover
  the exact remote status. Exit codes are the transport contract:
  0/1 → parse framed response, 2 → typed
  `ErrRemoteDaemonUnavailable`.
- **Daemon auto-start** (`ensure.go`) — when a relay hits exit 2, the
  hub probes `status --json` on the peer, starts a detached daemon
  (`nohup middleman serve`) if none runs, polls until the probe
  reports running **with published runtime metadata** (the api verb
  needs the listen address, which trails the lock early in startup),
  then retries the relay once. Double-starts are harmless: the loser
  exits on the remote runtime lock.

## Daemon contract for thin clients

Everything a local client (or the SSH relay acting as one) needs to
reach a daemon, with no out-of-band configuration:

- **Runtime discovery** (`internal/runtimelock`) — the daemon is
  running iff the flock on `<data_dir>/middleman.lock` is held; only
  then is `<data_dir>/middleman.run.json` authoritative. The metadata
  publishes pid, `listen_addr` (from the actual bound listener),
  `base_path` (canonical, no trailing slash — clients join API paths
  onto it), `token_path`, and `require_auth`.
- **Auth token** — minted at startup (32-byte hex, 0600, reused
  across restarts) at `<data_dir>/auth_token`. The file mode is the
  authorization boundary. With `[api] require_auth`, both the `/api/`
  routes and the `/ws/` terminal WebSocket routes demand
  `Authorization: Bearer <token>` or the session cookie that browsers
  bootstrap once via the tokenized URL (`/?auth_token=...` → HttpOnly
  cookie + redirect; browsers carry the cookie on the WebSocket
  upgrade). `/healthz` and `/livez` stay exempt so supervisors can poll
  before reading the token file. The hub never forwards a caller's
  token or cookie to an HTTP peer, so a require_auth daemon cannot also
  serve as an HTTP peer — reach it as an SSH peer instead.
- **`middleman api` verb** (`cmd/middleman/api_verb.go`) — the
  thin-client primitive: discovers the daemon through the runtime
  metadata, authenticates with the token, relays one request.
  Response bytes go to stdout verbatim; `-i` prefixes the exact
  status line; exit 0 on 2xx, 1 on other HTTP statuses, 2 when no
  request was made. The running daemon's published `base_path` is
  authoritative for the URL prefix (a config edit awaiting restart
  must not repoint the verb).
- **Host validation** — the TCP listener validates the `Host` header
  against the bind address and `allowed_hosts` (DNS-rebinding
  defense). An ephemeral bind (port 0 listeners passed to `Serve`)
  repoints the accept-set at the kernel-assigned port.

## Testing

- Wire-level tests per route (`internal/server/fleet_*_test.go`),
  including degraded-peer, cold-SSH-peer single-flight, attach-spec
  wrapping, and auto-start contracts against a faked ssh exec seam.
- `internal/sshfleet` unit tests pin the ControlMaster lifecycle,
  relay framing, and ensure-daemon polling.
- `cmd/middleman` e2e tests build the real binary and pin the api
  verb's auth, framing, exit-code, and base-path behavior.
- Container e2e (`fleet_container_e2e_test.go`,
  `scripts/e2e/fleet/`) runs a real hub + member over Docker
  networking and validates the read plane and availability policy
  end to end.
