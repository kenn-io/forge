# Merge Workspace Cleanup Design

**Date:** 2026-08-05
**Status:** Approved for implementation

## Goal

Let a user prune a pull request's linked workspace automatically after a
successful merge, including merges deferred until CI passes, without deleting
dirty or replacement workspaces and without obscuring cleanup failures.

## User Experience

When the pull request currently has a workspace, the merge dialog shows a
checkbox labeled **Delete workspace after merge**. The checkbox is absent when
the pull request has no workspace.

The checkbox remembers the user's last choice under the browser local-storage
key `kenn-forge:merge:delete-workspace-after-merge`. Its first-use default is
checked. Changing it updates the saved preference immediately so the next merge
dialog uses the new value.

The choice applies to both immediate merges and **Merge after CI is complete**.
The workspace is not deleted when a merge fails, when a deferred merge is
cancelled or superseded before it runs, or when its pending checks fail.

If the merge succeeds but pruning fails, the merge remains successful and the
UI shows a kit-ui warning notice stating that the workspace was not pruned and
including the server's useful failure reason. Deferred completion notifications
use the same distinction: merged successfully, but workspace cleanup failed.

## Cleanup Ownership and Identity

The server owns post-merge cleanup. Browser-owned cleanup would be unreliable
when a tab closes, disconnects, or misses a deferred-completion event.

The public merge request carries the boolean cleanup intent
`delete_workspace_after_merge`. When accepting an immediate or deferred merge,
the server resolves the pull request's currently linked local workspace and
captures that exact workspace ID in an internal cleanup plan. The plan does not
resolve the relationship again after the merge. This prevents a long-running
deferred merge from deleting a new workspace that replaced the original one
while CI was running.

If the checkbox is selected but the pull request no longer has a linked
workspace when the request is accepted, the cleanup plan is empty and the merge
continues normally. If the pinned workspace has already disappeared when
cleanup runs, cleanup is treated as already complete. A later workspace with a
different ID is left alone.

No database migration or durable cleanup queue is introduced. Deferred merges
are already in-memory work owned by the pull API handler; their captured cleanup
plan has the same lifecycle and shutdown behavior.

## Deletion Lifecycle

Post-merge pruning uses the same workspace deletion lifecycle as an ordinary
workspace delete. The lifecycle must coordinate setup admission, runtime
sessions, workspace-manager cleanup, persisted records, cache invalidation, and
client notification from one shared path rather than duplicating those steps in
the pull API.

Automatic pruning is always non-force. The existing dirty-file preflight stays
authoritative. If uncommitted files or another protected condition prevents
deletion, the workspace remains intact and the cleanup result carries the
failure reason. Selecting the merge-dialog checkbox does not authorize
discarding local changes.

The shared lifecycle emits an authoritative `workspace_deleted` event after a
successful delete. Its payload contains the exact workspace ID and the linked
pull-request identity. Connected clients use it to tombstone the exact
workspace ID, release matching inline claims, forget dead terminal routes,
refresh linked detail state, and remove the workspace from visible lists.
Existing explicit workspace deletion should use the same event so deletion
initiated on one client is visible to other connected clients consistently.

## Merge and Deferred-Merge Results

The immediate merge response gains an optional `workspace_cleanup` result,
present only when cleanup was requested. It contains the pinned `workspace_id`
when one existed, a `status` of `deleted`, `already_absent`,
`not_found_at_submission`, or `failed`, and an optional `warning` for the failed
state. The provider-confirmed merge fields remain authoritative; a cleanup
warning does not turn the HTTP response into a failed merge.

The deferred worker carries the same internal cleanup plan through its existing
CI polling and merge completion path. Its `deferred_merge_completed` event gains
the same optional `workspace_cleanup` result. The event is published only after
the cleanup attempt has finished, so its notification describes the terminal
merge-and-cleanup outcome accurately.

An immediate merge that supersedes a queued deferred merge creates and executes
its own cleanup plan. The superseded worker remains silent and must not run its
older cleanup plan.

## Error Handling

- Provider merge failure: return the existing merge error and do not attempt
  workspace deletion.
- Deferred CI failure, timeout, cancellation, or target change: keep the
  workspace and report the existing deferred-merge failure.
- Workspace already absent: treat pruning as successful and do not warn.
- Dirty workspace: preserve it and return a cleanup warning containing the
  dirty-workspace reason.
- Other workspace deletion failure: preserve whatever remains, log the
  server-side error, and return a useful cleanup warning.
- Successful merge and successful pruning: refresh merge-request data and
  invalidate the deleted workspace on all connected clients.

## Frontend Structure

The local-storage preference belongs in a small TypeScript helper so its
first-use default, malformed/unavailable storage behavior, and writes can be
tested without coupling those rules to modal rendering. Storage access is
best-effort: an unavailable local-storage implementation falls back to checked
without preventing a merge.

`PullDetail` passes current workspace presence into `MergeModal`. `MergeModal`
owns the editable checkbox state, includes the cleanup intent in both merge
request bodies, and forwards the immediate cleanup result to `PullDetail`.
Application-level deferred-completion handling reads the result from the event.
Both paths present a failed cleanup through the existing kit-ui flash system
with `warning` tone and copy that explicitly says the workspace was not pruned.

The application-level workspace-deleted event handler forwards the pinned ID
and item identity into the workspace host's existing deletion invalidation so
terminal and inline-workspace state cannot resurrect a pruned workspace from a
stale detail envelope.

## Testing

Focused frontend tests cover:

- the checkbox is visible only when the pull request has a workspace;
- first use is checked, the saved value is restored, and toggles persist;
- unavailable or malformed storage falls back safely;
- both immediate and deferred request bodies carry the selected intent;
- immediate cleanup failure closes the merge flow as successful and emits a
  kit-ui warning that the workspace was not pruned; and
- workspace-deleted events invalidate the exact workspace and refresh linked
  data.

Focused server tests cover:

- no cleanup attempt before provider-confirmed merge success;
- successful immediate and deferred merges prune the workspace through the
  shared non-force lifecycle;
- merge failure and deferred CI failure never prune;
- the workspace ID is pinned when the request is accepted and a replacement ID
  is not deleted;
- an already-absent pinned workspace is idempotent success;
- dirty workspaces remain and produce a cleanup warning without changing the
  successful merge result;
- an immediate merge superseding a deferred merge cannot trigger cleanup from
  the stale worker; and
- successful deletion broadcasts the workspace-deleted event used by connected
  clients.

Existing merge, deferred-merge, workspace deletion, inline-workspace, OpenAPI,
and generated-client verification remain the regression suite.
