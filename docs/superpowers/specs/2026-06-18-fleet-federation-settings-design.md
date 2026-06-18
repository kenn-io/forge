# Fleet Federation Settings Design

## Summary

Add a fleet federation settings panel that lets users keep local workspace
features enabled while disabling every remote fleet surface by default. The
settings panel manages the federation toggle, optional local fleet key, peer
timeout, session detail setting, and both HTTP and SSH peer membership.

The toggle controls remote federation only. Local workspace snapshots,
registered worktrees, runtime sessions, and local host operations continue to
work when federation is disabled. Saved peer membership remains in config so a
user can turn federation off temporarily and later re-enable the same members.

## Goals

- Make remote fleet federation opt-in, with a default disabled state.
- Hide remote fleet UI when federation is disabled, including the fleet
  indicator/status block in the workspace sidebar.
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
- Existing raw snapshot behavior remains local-only and is not affected by the
  toggle.

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

`GET /settings` can include this fleet shape for page bootstrap, but fleet
updates should use a focused route such as `PUT /settings/fleet`. This keeps
fleet validation, restart-required reporting, and peer replacement semantics
separate from activity, terminal, mode, and agent settings.

## Validation And Restart Semantics

Validation should preserve existing rules and add only the enable flag:

- `fleet.enabled` defaults to false.
- `fleet.key`, if present, is trimmed and must not collide with any peer key.
- Peer keys remain non-empty and unique across HTTP peers, SSH peers, and the
  local key when the local key is set.
- HTTP peer `base_url` must be an absolute `http` or `https` URL.
- SSH peer `destination` is required.
- SSH peer `remote_command`, when set, must remain a bare executable name or
  path with no flags or shell metacharacters.
- `peer_timeout`, when set, must parse as a Go duration.

Validation failures return the existing settings `badRequest` problem style.
Save failures return the existing internal settings save problem style.

Restart-required behavior:

- Toggling `fleet.enabled` applies immediately to snapshot fan-out and remote
  proxy resolution.
- Editing HTTP peers, `fleet.key`, or `peer_timeout` applies immediately to
  snapshot fan-out and HTTP proxy routing.
- Editing SSH peers still reports `restart_required` because the SSH transport
  is constructed at daemon startup.
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

## Workspace UI

When federation is disabled, workspace UI should read as single-host:

- `WorkspaceListSidebar` should not request peer-inclusive data for display, or
  should receive only the local host from the backend even when it asks for
  peers.
- The fleet host indicator/status block in the workspace sidebar should be
  hidden.
- Remote host filters, remote host rows, unreachable-peer diagnostics, and
  remote host operations should not appear.
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
- Remote fleet proxy resolution returns not found or unavailable for remote
  peers when disabled, while local self routing still works.
- `PUT /settings/fleet` validates peer collisions, HTTP URLs, SSH
  destinations, SSH remote commands, and peer timeout.
- `restart_required` is true for SSH peer and session detail edits, but false
  for enable toggle, key, timeout, and HTTP peer edits.

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
