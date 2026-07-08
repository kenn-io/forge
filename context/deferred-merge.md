# Deferred ("merge after CI") merge invariants

- Queued deferred merges live only in the server process (`deferredMergeInFlight`
  in `internal/server/deferred_merge.go`); a restart drops them. Detail responses
  expose the state as `deferred_merge_pending`.
- Terminal ordering contract: pending must be cleared **before** broadcasting a
  terminal `deferred_merge_completed` (success or failure). Clients refresh
  detail the moment they see the event, and that first read must not report a
  queued merge.
- A successful immediate merge supersedes the queued worker silently (per-key
  handle): pending clears with the merge response and the worker emits no event.
  A failed immediate merge leaves the queued merge untouched.
- The worker also stands down silently whenever it observes the target already
  merged (`errDeferredMergeTargetMerged`); the supersede handle alone cannot
  cover this because the worker syncs provider state independently and can see
  the merge before the immediate-merge path supersedes it. A closed (not
  merged) target still broadcasts a failure — closing is the user's cancel.
- In-flight cleanup is compare-and-delete on the per-key handle: terminal
  paths clear before broadcasting, so a stale worker's deferred cleanup must
  not remove a newer queue's handle for the same key.
- Closing the pull request is the user's only cancel for a queued deferred
  merge; queueing a second one returns 409 `already_pending`, so the UI must
  not offer deferred actions while `deferred_merge_pending` is true.
