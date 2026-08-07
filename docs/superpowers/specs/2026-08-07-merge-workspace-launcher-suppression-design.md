# Merge Workspace Launcher Suppression Design

## Problem

An immediate pull-request merge can delete its linked workspace inside the merge
request. The terminal view only marks deletion as pending when deletion starts
from its own controls. During merge-triggered teardown, runtime sessions can
disappear while the detail envelope still reports a ready workspace, so the
empty-pane fallback briefly opens the Launch a session dialog.

## Design

Extend the existing shared workspace lifecycle store with deletion-pending state
keyed by workspace ID and host key. `MergeModal` will begin this lifecycle only
for an immediate merge whose submitted body includes `delete_workspace_id`.
It will end the lifecycle on request failure. When the merge succeeds without a
workspace cleanup warning, it will first mark the requested workspace ID as
deleted and then clear the pending state.

`WorkspaceTerminalView` will treat a workspace as non-launchable while shared
deletion is pending or its ID is confirmed deleted. This supplements its current
component-local delete state and covers both the teardown interval inside the
merge request and the short interval after the response while PR detail refresh
removes the stale workspace envelope.

Deferred merge submission will not hold deletion-pending state while waiting for
CI because the workspace remains usable during that potentially long interval.
This change covers the immediate merge interaction where teardown occurs inside
the user-triggered request.

## Error Handling

If the merge request fails, throws, or reports a cleanup warning, shared pending
state is cleared without marking the workspace deleted. The workspace therefore
returns to normal launcher behavior. A cleanup warning remains the existing
user-visible warning and means the workspace still exists.

## Testing

- Store tests will cover host-aware pending deletion begin/end behavior.
- `MergeModal` tests will verify that immediate cleanup publishes pending state,
  success marks the workspace deleted, and failure releases pending state.
- `WorkspaceTerminalView` tests will reproduce the empty-runtime transition and
  verify the launcher stays closed for both pending and confirmed deletion.
- The full frontend unit suite will run after the final frontend/test edit.

## Scope

No API or backend contract changes are required. The fix does not change merge,
workspace deletion, launcher selection, or deferred-merge behavior beyond
suppressing the invalid launcher presentation during immediate cleanup.
