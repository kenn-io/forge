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
- Closing the pull request is the user's only cancel for a queued deferred
  merge; queueing a second one returns 409 `already_pending`, so the UI must
  not offer deferred actions while `deferred_merge_pending` is true.
