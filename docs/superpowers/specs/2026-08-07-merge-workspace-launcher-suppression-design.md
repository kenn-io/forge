# Workspace Lifecycle Launcher Suppression Design

## Problem

An immediate pull-request merge can delete its linked workspace inside the merge
request. The terminal view only marks deletion as pending when deletion starts
from its own controls. During merge-triggered teardown, runtime sessions can
disappear while the detail envelope still reports a ready workspace, so the
empty-pane fallback briefly opens the Launch a session dialog.

A related race exists after choosing an agent from the Create Workspace split
button. The create-and-launch intent is published, but the terminal can clear
that intent before the launched session is present in refreshed runtime state.
The ready workspace then looks like an ordinary empty workspace and the generic
Launch a session dialog opens, asking the user to choose Claude, Codex, or a
terminal after they already chose Codex.

## Design

Extend `packages/ui/src/stores/workspace-create-pending.svelte.ts`, the existing
shared workspace lifecycle store, with deletion-pending state keyed by workspace
ID and workspace host key. `MergeModal` will begin this lifecycle only for an
immediate merge whose submitted body includes `delete_workspace_id`. Merge-linked
inline workspaces are local, so this entry always uses an undefined workspace
host key; the modal's `platformHost` is provider identity and must not be used as
workspace-host identity. The modal will end pending state on request failure.
When the merge succeeds without a workspace cleanup warning, it will first
publish successful deletion and then clear pending state.

`WorkspaceTerminalView` will treat a workspace as non-launchable while shared
deletion is pending or its ID is confirmed deleted. This supplements its current
component-local delete state and covers both the teardown interval inside the
merge request and the short interval after the response while PR detail refresh
removes the stale workspace envelope.

Shared deletion pending and confirmed-deleted state will join the terminal's
existing deletion gate, so `actionsBlocked` disables launch, delete, refresh, and
other workspace mutations during teardown. A workspace being destroyed is not a
valid target for any mutation, and this prevents actions from racing the merge
request rather than suppressing only the automatic launcher.

Successful merge cleanup must use the same full deletion pathway as every other
delete surface. `MergeModal` will report the exact deleted workspace ID only when
cleanup was requested and succeeded. `PullDetail` will pass that ID and its item
identity through a Provider-supplied deletion callback; the frontend binds that
callback to `notifyWorkspaceDeleted`. This tombstones every relevant inline
claim, invalidates detail consumers, drops promoted session panes, marks the ID
deleted, clears created-workspace state, and forgets remembered terminal routes
before merge presentation continues. `inlineWorkspace.recordDeleted` alone is
insufficient because it updates only the current surface.

The explicit create-and-launch intent will use three store-owned phases: queued,
launching, and awaiting-session. Claiming a queued intent moves it to launching
with an ownership token. A successful launch response publishes the returned
session key before any component-liveness guard and moves the intent to
awaiting-session without a claim token. A replacement or revisited terminal can
therefore reconcile the intent without relaunching the agent. A failed launch
settles the token and removes the intent. An in-flight request remains owned by
the initiating async operation across component unmount; that operation must
publish success or failure even if its original view is no longer current.

Awaiting-session state remains authoritative until refreshed runtime contains
the returned session key. The live terminal that observes it mounts and selects
the session, requests focus, and completes the intent. A successful launch
response alone is not sufficient to clear the intent when the immediately
refreshed runtime is still empty. During this handoff, the workspace remains in
its existing launching presentation and the automatic generic launcher stays
closed.

Awaiting-session reconciliation is bounded to 15 seconds from the successful
launch response. Runtime refresh and event updates continue to look for the exact
returned session key during that window. If it never appears — including when an
agent exits during startup and backend lifecycle cleanup removes it before any
refresh observes it — the current live view settles the intent and shows one
danger flash explaining that the launched session did not become available. The
timeout makes every accepted launch reclaimable across navigation while avoiding
a permanently suppressed launcher.

Deferred merge submission will not hold deletion-pending state while waiting for
CI because the workspace remains usable during that potentially long interval.
This change covers the immediate merge interaction where teardown occurs inside
the user-triggered request. A queued deferred merge can still expose the same
empty-runtime flash when its background worker eventually deletes the workspace;
closing that residual gap requires a server-side cleanup-start signal and remains
outside this frontend-only change.

## Error Handling

If the merge request fails, throws, or reports a cleanup warning, shared pending
state is cleared without marking the workspace deleted. The workspace therefore
returns to normal launcher behavior. A cleanup warning remains the existing
user-visible warning and means the workspace still exists.

If create succeeds but the selected agent launch fails, the create-and-launch
intent must settle so the workspace does not remain permanently pending. A
successful launch whose session has not appeared yet remains pending for the
bounded reconciliation window and relies on the existing runtime refresh/event
flow; it must not fall back to the generic chooser in the meantime. The current
immediate `Session launched, but the workspace could not be reloaded` danger flash
will be removed: one empty refresh is an expected eventual-consistency interval,
not a terminal failure. Only request failure or reconciliation timeout produces
an error.

## Testing

- Store tests will cover host-aware pending deletion begin/end behavior.
- Store tests will cover queued, claimed, awaiting-session, completed, and failed
  explicit launch transitions, including publication after the initiating view
  is no longer current.
- `MergeModal` tests will verify that immediate cleanup publishes pending state,
  success reports the exact local workspace deletion, unchecked cleanup reports
  none, and failure or cleanup warning releases pending state without deletion.
- Provider/PullDetail tests will verify successful cleanup reaches the app-level
  deletion callback with the workspace ID and item identity.
- `WorkspaceTerminalView` tests will reproduce the empty-runtime transition and
  verify all workspace actions and the launcher stay blocked for both pending and
  confirmed deletion.
- `WorkspaceTerminalView` tests will also reproduce a successful create-and-launch
  response followed by an empty runtime refresh, verify the generic launcher
  remains closed across remount, then publish the selected session and verify the
  intent settles without a duplicate launch.
- A terminal test will advance past the 15-second reconciliation window without
  publishing the session, verify the intent settles, and verify the delayed danger
  feedback replaces the current first-refresh error.
- The full frontend unit suite will run after the final frontend/test edit.

## Scope

No API or backend contract changes are required. The fix does not change merge,
workspace deletion, agent selection, or deferred-merge behavior. It only keeps
the terminal's presentation aligned with deletion and explicit launch lifecycle
state while asynchronous runtime state catches up.
