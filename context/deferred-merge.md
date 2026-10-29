# Deferred ("merge after CI") merge invariants

Use this document for changes to deferred merge queueing, cancellation,
supersession, completion events, or pending-state presentation.

- Stack position alone does not warrant a merge warning. With mid-stack merges
  allowed, gh-stack users may queue merges at any position; show neutral stack
  context rather than cautioning about merge order.
- The merge block follows the merge credential's view. `rules`, read as the merge credential
  (GitHub answers for the asker), is `clear`, `blocked_bypassable`, `blocked`, or `unknown`.
  `rules` applies only while the stored provider state is `blocked`; otherwise it is `clear`.
  The merge window treats `unknown` (unread or failed) as `clear`, never as bypassable: the sync
  credential's `blocked` does not speak for the merge credential, and GitHub refuses a merge its
  rules block. Other providers keep their provider state
  (`frontend/src/lib/components/detail/merge-policy.ts::mergeRules`,
  `internal/github/merge_bypass.go::Syncer.ReadMergeRules`).
- GitHub, not Forge, enforces the rules on the merge. Its asynchronous merge API merges over
  rules the merging credential may bypass only when sent `bypass_rules`, so every immediate and
  deferred merge sends the user's `consent` there; stored `rules` only decide what Forge offers,
  and a stale answer at worst offers a merge GitHub refuses. GitHub Enterprise Server has only
  the synchronous merge endpoint, which takes no bypass choice
  (`platform/github/merge_async.go::Client.MergePullRequest`).
- GitHub reports BLOCKED while required checks are still pending, so during `ci=pending`,
  `blocked` does not mean the merge needs a bypass; it may clear once CI passes.
- A merge queue target is exempt from `rules`. `consent` is the user's bypass tick; a deferred
  merge fixes it at queue time as `bypass_rules`.

Merge window and button, immediate merge, not a queue target
(`frontend/src/lib/components/detail/merge-policy.ts::mergeActionState`):

| ci | rules | Merge button | "Merge after CI" | Immediate merge |
| --- | --- | --- | --- | --- |
| failed | any | as today (CI failed) | no | as today |
| pending | any | enabled | offered, no tick needed | `clear`: as today. `blocked_bypassable`: needs the tick. `blocked`: unavailable. |
| passed | clear | enabled | n/a | allowed |
| passed | blocked_bypassable | enabled | n/a | needs the tick |
| passed | blocked | disabled, with the #1599 reason | n/a | unavailable |

Show the bypass checkbox only for `blocked_bypassable`. The list menu follows the same table. It
has no checkbox, so for `blocked_bypassable` with CI passed, the list says the PR needs a bypass
and to merge it from the PR page. With a merge already queued, a PR whose immediate merge is
unavailable keeps the button disabled, since the modal then offers only an immediate merge.

Deferred merge worker (`internal/server/pullapi/deferred_merge.go::deferredMergeNextStep`):

- **WaitingForCI**
  - CI fails: Failed("a current CI check failed"), as today.
  - CI passes: go to Recheck.
- **Recheck.** Keep the existing target-unchanged, closed, merged-elsewhere, and superseded
  checks. Then read `rules` as the merge credential.
  - `queue`: enqueue, as today.
  - `clear`: Merge.
  - Blocked (either kind) with `consent`: Merge. This is the only path that merges over the rules.
  - Blocked (either kind) without `consent`: Failed("merge needs a bypass of branch protection or
    repository rules; it was not performed"). Do not keep waiting.
  - `unknown`: retry on the next tick within the existing wait budget. When the budget runs out,
    fail with a message saying Forge could not confirm the merge state.
- **Merge:** succeeds as Merged, or reports the provider's refusal as Failed, as today.

Recheck reads `rules` fresh as the merge credential when the provider read it just made
reports `blocked`; any other provider state is `clear` with no extra read, as before. It never
trusts the sync credential's read alone, which under split auth reports BLOCKED forever. A host
that can never answer (no GraphQL credential, or a schema without the fields) keeps that provider
read; a missing queue state is read only when `rules` is not `clear`
(`internal/server/pullapi/deferred_merge.go::Handler.deferredMergeRules`).
- Queued deferred merges live only in the server process (`deferredMergeInFlight`
  in `internal/server/pullapi/deferred_merge.go`); a restart drops them. Detail responses
  expose the state as `deferred_merge_pending`.
- Terminal ordering contract: pending must be cleared **before** broadcasting a
  terminal `deferred_merge_completed` (success or failure). Clients refresh
  detail the moment they see the event, and that first read must not report a
  queued merge.
- A successful immediate merge or merge queue enqueue supersedes the queued
  worker silently (per-key handle): pending clears with the merge response and the worker emits no event.
  An immediate provider response with `merged: false` leaves the queued merge and
  all merge-completion side effects untouched.
- Immediate and deferred merges and enqueues hold a per-pull-request merge
  lock through the provider call. The deferred worker checks supersession
  under that lock, because an enqueue leaves the pull request open where
  target checks cannot see it (`internal/server/pullapi/routes.go::Handler.mergePRWithBodyAdmitted`).
- A deferred worker whose merge went to the target branch's merge queue
  completes with status `queued`: clear pending, publish no merged status, and
  leave the outcome to the pull request's queue state; its workspace deletion is
  kept like an immediate enqueue's
  (`internal/server/pullapi/deferred_merge.go::Handler.completeDeferredMerge`).
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
- Queue acknowledgement refreshes deferred pending state; it must never imply
  provider merge completion or workspace deletion
  (`frontend/src/lib/stores/detail.svelte.ts::MergePullOutcome`).
