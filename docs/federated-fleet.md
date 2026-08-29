# Federated fleet

A fleet combines several Kenn Forge daemons in one UI. The daemon you open is
the **hub**. Remote daemons are **peers**.

The hub shows repositories, worktrees, sessions, hosts, and live tmux state.
Supported actions route back to the peer that owns the resource. Every machine
runs the same `kenn-forge` binary.

Fleet links are one hop from hub to peer. Peers do not discover or relay
through other peers.

## Choose a transport

| Transport | Use it when | Required boundary |
| --- | --- | --- |
| HTTP | The peer listener is reachable from the hub | Loopback, tailnet, or another trusted VPN |
| SSH | The peer should not expose a listener or requires authentication | Working non-interactive SSH access |

HTTP fleet requests do not carry a bearer token. The hub also strips the
caller's authorization and cookies. An HTTP peer must leave
`[api].require_auth` disabled.

Use SSH for any peer that requires API authentication. On Unix, the hub owns
one SSH ControlMaster per peer through kit's shared OpenSSH lifecycle manager.
When multiplexing is unavailable, including on Windows clients, it uses an
explicit masterless connection instead. Both modes run the remote Forge
CLI through the user's system OpenSSH policy.

## Prepare each machine

1. Install `kenn-forge` on the hub and every peer.
2. Give each daemon a unique fleet key.
3. Start each peer with its own repositories and workspaces configured.
4. Confirm the hub can reach the HTTP listener or SSH destination.

Remote binary installation is not automatic. SSH peers can start an installed
daemon when needed, but they cannot install or upgrade it.

## Configure the hub

```toml
[fleet]
enabled = true
key = "studio"
peer_timeout = "2s"

[fleet.sessions]
include_unmanaged_details = false

[[fleet.peers]]
key = "desktop"
name = "Desktop"
base_url = "http://desktop.internal.example:8091"

[[fleet.ssh_peers]]
key = "build"
name = "Build host"
destination = "maintainer@build.internal.example"
remote_command = "kenn-forge"

[api]
require_auth = true
```

`fleet.enabled` turns on remote federation. `fleet.key` identifies this host.
An empty host key creates an anonymous member that cannot meaningfully host.
Peer keys must be non-empty and unique across all fleet entries.
`fleet.peer_timeout` defaults to `2s`.

`include_unmanaged_details = false` limits unregistered tmux sessions to names
and window counts. Enable it only when the hub may show full unmanaged session
details.

The hub may protect its own API with `require_auth = true`. An HTTP peer may
not. The HTTP peer must travel over a trusted private network.

For SSH peers, `destination` accepts a validated `[user@]host`, optional port,
IPv6 literal, or `ssh://` URI. Host aliases still use the user's `ssh_config`;
put unusual account names behind a safe alias rather than in the programmatic
destination. `remote_command` must be a bare executable name or path without
flags or shell syntax. Set `KENN_FORGE_HOME` in the remote login profile when
the peer uses another config directory.

Restart the hub after changing API authentication or SSH peer entries.

## Open an authenticated hub

When `require_auth = true`, read the hub token from
`<data_dir>/auth_token`. The default path is
`~/.kenn/forge/auth_token`.

Open the hub once with the token in the query string:

```text
http://127.0.0.1:8091/?auth_token=<token>
```

Forge exchanges the token for an HttpOnly browser cookie, then redirects
to the clean URL. The cookie also authenticates terminal WebSocket requests.
Do not share the tokenized URL.

## Use the fleet

Open the hub UI. Fleet-aware views show the host that owns each resource.
Create or delete workspaces, launch or stop sessions, and open terminals from
the same controls used for local resources.

Remote operations preserve the peer's response. If the hub cannot route an
operation, the UI disables it and explains why.

An unreachable peer does not break the fleet view. Its host remains visible
with `reachable: false` and an error. Other hosts continue to load.

The first request to a cold SSH peer may report **connection warming**. The
hub continues opening the shared connection in the background. Retry after the
host state changes to connected.

## Work with sessions

Each daemon reports its own tmux sessions. Registered workspace sessions are
marked as managed. Unregistered sessions stay redacted unless
`include_unmanaged_details` is enabled.

An attach action returns the native tmux command for a local session. For an
SSH peer, the hub wraps that command through the generation-checked shared
connection, or through an explicit masterless SSH command when multiplexing is
unavailable. Clients do not create their own SSH master.

Persistent SSH masters use keepalives and close after 30 minutes without
activity. On Unix, a securely owned socket left by a previous hub process is
reused only when OpenSSH verifies that it is still alive.

## Troubleshoot a peer

### HTTP peer is unreachable

Check `base_url` from the hub machine. Confirm the route stays on a trusted
network and the peer does not require API authentication. A slow peer must
answer within `fleet.peer_timeout`.

### SSH peer does not connect

Test the exact destination from the hub:

```sh
ssh -o BatchMode=yes maintainer@build.internal.example true
```

Confirm `kenn-forge` is on the remote login `PATH`. If the peer uses another
home directory, set `KENN_FORGE_HOME` in that login profile.

When the relay finds no running daemon, it checks remote status and starts
`kenn-forge serve`. It waits for runtime metadata, then retries once.

### Remote action is unavailable

Check the host diagnostic in the UI. Repository registry changes remain local
to each host. Workspace and session actions require a reachable HTTP or SSH
route.

### Attach is unavailable

The session must exist in Forge and in tmux. Non-tmux sessions do not
provide native attach commands.
