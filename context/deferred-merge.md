# Deferred ("merge after CI") merge invariants

Use this document for changes to deferred merge queueing, cancellation,
supersession, completion events, or pending-state presentation.

- Queued deferred merges live only in the server process (`deferredMergeInFlight`
  in `internal/server/pullapi/deferred_merge.go`); a restart drops them. Detail responses
  expose the state as `deferred_merge_pending`.
- Terminal ordering contract: pending must be cleared **before** broadcasting a
  terminal `deferred_merge_completed` (success or failure). Clients refresh
  detail the moment they see the event, and that first read must not report a
  queued merge.
- A successful immediate merge supersedes the queued worker silently (per-key
  handle): pending clears with the merge response and the worker emits no event.
  An immediate provider response with `merged: false` leaves the queued merge and
  all merge-completion side effects untouched.
- A deferred worker response with `merged: false` is a terminal failure: clear pending and publish the provider message,
  but never publish merged status or run merge-completion side effects
  (`internal/server/pullapi/deferred_merge.go::Handler.completeDeferredMerge`).
- The worker also stands down silently whenever it observes the target already
  merged (`errDeferredMergeTargetMerged`); the supersede handle alone cannot
  cover this because the worker syncs provider state independently and can see
  the merge before the immediate-merge path supersedes it. A closed (not
  merged) target still broadcasts a failure — closing is the user's cancel.
- In-flight cleanup is compare-and-delete on the per-key handle: terminal
  paths clear before broadcasting, so a stale worker's deferred cleanup must
  not remove a newer queue's handle for the same key.
- Spoke preparation rejects new deferred-merge admission and counts every
  already-admitted handle until compare-and-delete cleanup or immediate-merge
  supersession reaches a known terminal outcome. The preparation acknowledgement
  generation cannot freeze while this count is nonzero
  (`internal/server/pullapi/deferred_merge.go::deferredMergeHandle.finish`).
- Spoke role never constructs local provider or deferred-merge workers. Active
  spokes proxy deferred merges through a validated hub client;
  inactive or incompatible spokes reject them without federation egress
  (`cmd/kenn-forge/provider_startup.go::buildServeControlPlanes`,
  `internal/server/server.go::Server.serveProviderRoute`).
- Closing the pull request is the user's only cancel for a queued deferred
  merge; queueing a second one returns 409 `already_pending`, so the UI must
  not offer deferred actions while `deferred_merge_pending` is true.
- Deferred merges retain workspace ID and authenticated originating spoke before
  background execution; only success admits cleanup on the owning daemon
  (`internal/server/pullapi/deferred_merge.go::Handler.enqueueDeferredMerge`).
- Successful completion can carry `workspace_cleanup_pending`; cleanup completion
  is reported independently by workspace lifecycle events, so the merge event
  never infers deletion (`internal/server/pullapi/deferred_merge.go::DeferredMergeCompletedPayload`).
- Frontend callbacks distinguish queue acknowledgement from provider merge
  completion. A queued outcome closes the modal and refreshes pending state; it
  never publishes workspace deletion (`frontend/src/lib/stores/detail.svelte.ts::MergePullOutcome`).
