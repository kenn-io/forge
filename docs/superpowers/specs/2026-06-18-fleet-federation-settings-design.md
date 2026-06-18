# Fleet Federation Settings Design

## Summary

Add a fleet federation settings panel that lets users keep local workspace
features enabled while disabling every remote runtime, status, and operation
surface by default. The settings panel manages the federation toggle, optional
local fleet key, peer timeout, session detail setting, and both HTTP and SSH
peer membership.

The toggle controls remote federation only. Local workspace snapshots,
registered worktrees, runtime sessions, and local host operations continue to
work when federation is disabled. Saved peer membership remains visible and
editable in settings so a user can turn federation off temporarily and later
re-enable the same members.

## Goals

- Make remote fleet federation opt-in, with a default disabled state.
- Hide remote runtime/status/operation fleet UI when federation is disabled,
  including the fleet indicator/status block in the workspace sidebar.
- Preserve current local workspace behavior when federation is disabled.
- Let users configure both HTTP peers and SSH peers from the settings UI.
- Preserve existing config flexibility: `fleet.key` remains optional, but the
  UI explains why a stable key is recommended for hubs.
- Keep HTTP peer risk visible: HTTP federation requires a trusted transport
  boundary because the hub does not forward caller auth to peers.

## Non-Goals

- Do not disable the local snapshot or local workspace runtime layer.
- Do not provision, install, or bootstrap remote middleman daemons.
- Do not change the one-hop hub-to-peer topology.
- Do not delete peer membership when federation is disabled.
- Do not add compatibility aliases or legacy config paths.

## Configuration Model

Extend `config.Fleet` with an explicit enable flag:

```toml
[fleet]
enabled = false
key = "studio"
peer_timeout = "2s"
```

`enabled` defaults to `false` when omitted. Existing configs with peer entries
but no `enabled` flag will load with saved membership intact but remote
federation disabled until the user opts in. This is the intended new default.

The rest of the fleet config remains structurally the same:

- `key`: optional local host key. If empty, runtime behavior keeps the current
  hostname fallback. The settings UI warns that hubs should set a stable key.
- `peer_timeout`: optional Go duration, defaulting through existing
  `PeerTimeoutOrDefault`.
- `sessions.include_unmanaged_details`: existing tmux detail redaction flag.
- `[[fleet.peers]]`: HTTP members with `key`, optional `name`, and `base_url`.
- `[[fleet.ssh_peers]]`: SSH members with `key`, optional `name`,
  `destination`, optional `platform`, and optional `remote_command`.

Membership can be edited while federation is disabled. Saving membership while
disabled only changes persisted config; it does not make remote hosts visible.
This means the settings page remains a configuration surface even while the
runtime fleet surface is off.

## Backend Behavior

Remote federation gates should be centralized in the existing fleet paths:

- `buildFleetSnapshot(ctx, includePeers)` always builds the local raw snapshot.
  It fetches HTTP peers and SSH peers only when both `includePeers` and
  `cfg.Fleet.Enabled` are true.
- `resolveFleetHostTarget(hostKey)` keeps resolving the local self alias and
  local host behavior. It resolves HTTP or SSH peer targets only when
  `cfg.Fleet.Enabled` is true.
- Remote proxy routes therefore become unavailable while federation is
  disabled, even if peer membership is still saved.
- Disabled remote proxy routes return the existing unknown-host contract:
  `404`/`notFound` with `details.hostKey`. This applies consistently to HTTP
  proxy routes, WebSocket terminal proxy routes, filesystem routes, runtime
  routes, and mutation routes. Disabled configured peers intentionally collapse
  to the same client-visible behavior as unknown hosts, rather than reporting
  peer-unreachable or capability errors.
- Existing raw snapshot behavior remains local-only and is not affected by the
  toggle.
- Disabled federation must avoid HTTP and SSH peer network calls entirely.

The settings API should expose a focused fleet settings shape rather than
folding peer editing into unrelated general settings updates:

```json
{
  "enabled": false,
  "key": "studio",
  "peer_timeout": "2s",
  "sessions": {
    "include_unmanaged_details": false
  },
  "peers": [
    { "key": "mini", "name": "Mac mini", "base_url": "http://mini.tail:8091" }
  ],
  "ssh_peers": [
    {
      "key": "epyc",
      "name": "EPYC box",
      "destination": "wes@epyc.tail",
      "platform": "linux",
      "remote_command": "middleman"
    }
  ],
  "restart_required": false
}
```

`GET /settings` must include this fleet shape for page bootstrap, and
`GET /settings/fleet` returns the same fleet shape for clients that want to read
only federation settings. Fleet updates use `PUT /settings/fleet`. This keeps
fleet validation, restart-required reporting, and peer replacement semantics
separate from activity, terminal, mode, and agent settings.

`PUT /settings/fleet` is a full replacement of the fleet settings object, not a
patch. The server validates the complete candidate config before saving,
persists it through the existing atomic config writer, updates live in-memory
config only for successfully saved values, and rolls back any live in-memory
change if persistence fails. Concurrent saves use the same settings lock as the
other settings routes. Success always returns the stable fleet settings shape
above, including `restart_required`.

The existing `/settings/fleet/ssh-peers` endpoint remains supported for clients
that only edit SSH fleet peers. It is not a new compatibility alias; it is an
existing narrower settings surface. Both endpoints validate through the full
config, persist through the same config writer, roll back on failure, and report
restart drift against the same live SSH transport membership. New clients should
prefer `/settings/fleet` when they need the complete federation settings shape.

## Validation And Restart Semantics

Validation should preserve existing rules and add only the enable flag:

- `fleet.enabled` defaults to false.
- `fleet.key`, if present, is trimmed and must not collide with any peer key.
  The key `self` is reserved for local self routing and is invalid as a local,
  HTTP peer, or SSH peer key.
- Peer keys remain non-empty and unique across HTTP peers, SSH peers, and the
  local key when the local key is set.
- HTTP peer `base_url` must be an absolute `http` or `https` URL.
- SSH peer `destination` is required.
- SSH peer `remote_command`, when set, must remain a bare executable name or
  path with no flags or shell metacharacters.
- `peer_timeout`, when set, must parse as a Go duration.

Validation failures return the existing settings `badRequest` problem style.
Save failures return the existing internal settings save problem style. Disabled
remote proxy access returns `notFound`, including WebSocket, filesystem,
runtime, and mutation proxy routes. Enabled but unreachable peers keep using the
existing peer health and proxy error taxonomy.

`restart_required` reports fleet startup drift that cannot be applied to the
running daemon without restart. Both `/settings/fleet` and
`/settings/fleet/ssh-peers` use the same SSH peer drift calculation against the
running SSH transport membership.

Restart-required behavior:

- Toggling `fleet.enabled` applies immediately to snapshot fan-out and remote
  proxy resolution.
- Editing HTTP peers, `fleet.key`, or `peer_timeout` applies immediately to new
  snapshot fan-out and HTTP proxy routing requests. Changing `fleet.key`
  changes the local host key emitted by subsequent snapshots; clients should
  refresh settings/snapshot data after save, and old explicit host-key routes
  are not compatibility aliases. The generic self/local route behavior remains
  available.
- Editing SSH peers still reports `restart_required` because the SSH transport
  is constructed at daemon startup. Newly saved SSH peer membership is shown in
  settings immediately, but the live SSH transport continues to use the startup
  peer set until restart. Enabling federation before restart exposes only the
  live startup SSH peers, not newly persisted SSH peers.
- Editing `fleet.sessions.include_unmanaged_details` still reports
  `restart_required` because tmux monitoring is constructed at daemon startup.

## Settings UI

Add a `Fleet federation` section to the existing settings page under the
Workspace group. Use the integrated layout:

- A top-level toggle labeled around federation, not local workspaces. Suggested
  copy: `Enable fleet federation`.
- A short disabled-state explanation: remote hosts and remote operations are
  unavailable while local workspaces continue to work.
- Optional local key field with a warning or helper text that hubs should set a
  stable key.
- Peer timeout field.
- Existing unmanaged tmux details setting.
- HTTP peer membership editor.
- SSH peer membership editor.
- Save/reset controls consistent with current settings sections.

Membership editors stay visible while disabled, but the section should make it
clear that saved members are inactive until federation is enabled. This lets a
user prepare membership before turning the toggle on.

Both peer editors should support `key` and optional `name`. HTTP peers edit
`base_url`. SSH peers edit `destination`, optional `platform`, and optional
`remote_command`. The SSH editor should make the default remote command
behavior clear without requiring the user to enter `middleman`.

HTTP peer rows should include restrained warning text that HTTP federation is
for trusted network boundaries because the hub does not forward auth to peers.
Existing users who already have peers but no explicit `enabled` flag see the
disabled toggle and saved memberships in this settings section; no separate
first-run migration banner is required.

## Workspace UI

When federation is disabled, workspace UI should read as single-host:

- `WorkspaceListSidebar` should not request peer-inclusive data for display, or
  should receive only the local host from the backend even when it asks for
  peers.
- The fleet host indicator/status block in the workspace sidebar should be
  hidden.
- Remote host filters, remote host rows, unreachable-peer diagnostics, and
  remote host operations should not appear.
- Stale remote route state should be cleared or ignored when the current
  snapshot contains only the local host, so disabled federation cannot leave
  remote operation controls reachable from old UI state.
- Local workspace rows, local runtime sessions, and local create/delete/retry
  actions remain visible.

When federation is enabled, the current multi-host workspace sidebar behavior
returns: host status appears, remote workspaces load from reachable peers, and
remote operations use hub-keyed routes.

## Testing

Backend tests:

- Config parsing defaults `fleet.enabled` to false and round-trips true/false.
- Settings save preserves peer membership while toggling enabled off.
- `GET /snapshot?include_peers=true` excludes HTTP and SSH peers when disabled.
- The same snapshot includes configured peers when enabled.
- Remote fleet proxy resolution returns `404`/`notFound` for remote peers when
  disabled, while local self routing still works. This includes a representative
  REST route and the shared WebSocket-resolution path through the same target
  resolver.
- `PUT /settings/fleet` validates peer collisions, HTTP URLs, SSH
  destinations, SSH remote commands, and peer timeout.
- `restart_required` is true for SSH peer and session detail edits, but false
  for enable toggle, key, timeout, and HTTP peer edits.
- Full-stack API tests exercise `GET /settings`, `GET /settings/fleet`,
  `PUT /settings/fleet`, disabled snapshot behavior, enabled snapshot behavior,
  and disabled remote proxy behavior through the real HTTP server, temp TOML
  config path, and SQLite-backed workspace data where snapshot behavior needs
  it.

Frontend tests:

- Settings page shows the fleet federation section and default disabled state.
- Saving `enabled=false` keeps configured membership visible in the editor.
- HTTP and SSH peer validation errors surface through the existing settings
  error UI.
- Workspace sidebar hides the fleet status block when federation is disabled.
- Workspace sidebar shows the fleet status block when federation is enabled
  and multiple hosts are present.

Use Vitest + jsdom for settings and workspace sidebar behavior. Browser tests
are not required unless implementation changes layout behavior that needs real
geometry verification.

## Implementation Plan

Implement the feature in reviewable stages:

1. Add config model/default coverage for `fleet.enabled`.
2. Gate backend snapshot fan-out and remote proxy resolution.
3. Add focused settings API read/update behavior and generated client types.
4. Add the fleet settings editor under Workspace settings.
5. Hide workspace sidebar fleet status when only the local host is visible.
6. Add full-stack, unit, and frontend regression coverage.
