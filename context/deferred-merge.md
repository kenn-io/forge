# Deferred ("merge after CI") merge invariants

Use this document for changes to deferred merge queueing, cancellation,
supersession, completion events, or pending-state presentation.

## Merge rules

- Stack position alone does not warrant a merge warning; gh-stack users may queue merges at
  any position, so show neutral stack context rather than cautioning about merge order.
- The merge dialog for a native stack with more than one open member chooses the merge target,
  starting at the current pull request. Another target pins its stack entry's
  `reviewed_head_sha` as it was when the dialog opened, while its CI and draft state follow
  detail refreshes. Deleting the current pull request's workspace is offered only for a merge of
  that pull request, whose request replaces a deletion an earlier merge of it kept. A merge queue merge still skips the dialog when it asks nothing, unless a
  member below has failed or pending CI (`frontend/src/lib/components/detail/MergeModal.svelte`). The detail
  store orders a merge of another member under that member's mutation key and refreshes the
  shown pull request (`frontend/src/lib/stores/detail.svelte.ts::runPullAction`).
- Merge warnings state a current fact, never "may": warn about rules only when the merge
  credential's merge is blocked now, not during pending CI or before its `rules` arrive
  (`frontend/src/lib/components/detail/PullDetail.svelte::mergeWarningEntries`).
- The merge block follows the merge credential's view, never the sync credential's alone;
  GitHub answers for the asker (`internal/github/merge_bypass.go::Syncer.ReadMergeRules`).
- `rules` is `clear`, `blocked_bypassable`, `blocked`, or `unknown`, and applies only while the
  stored provider state is `blocked` (`frontend/src/lib/components/detail/merge-policy.ts::mergeRules`).
- With `MergeCredentialRules` (github.com only), `unknown` counts as `clear`, never bypassable: the
  sync credential's `blocked` does not speak for the merge credential, and GitHub refuses a blocked merge.
- Elsewhere `unknown` keeps the provider's `blocked`: GitLab's state is the same for every caller,
  and Enterprise Server's synchronous merge bypasses without asking.
- GitHub, not Forge, enforces the rules on the merge. Stored `rules` only decide what Forge
  offers; a stale answer at worst offers a merge GitHub refuses.
- Bypassing branch protection or repository rules is always an explicit user choice (`consent`);
  every immediate and deferred merge with it ticked sends it as `bypass_rules`, and a deferred
  merge fixes it at queue time (`platform/github/merge_async.go::Client.MergePullRequest`).
- GitHub Enterprise Server has only the synchronous merge endpoint, which takes no bypass choice.
- On github.com a merge uses the synchronous endpoint only with no bypass tick, outside any stack
  (native or branch-inferred), after a queue read found no queue on the base branch, and while the
  stored state is `clean`, `unstable`, `has_hooks`, or `blocked` with `rules` `clear`, and only when
  a merge state read as the merge credential, made before the deferred worker's last supersession
  check, returns `CLEAN`, `UNSTABLE`, or `HAS_HOOKS`. A dismissed review or an advanced base blocks
  the merge without changing the head, so stored state can be stale, and the rules read counts
  `BEHIND` as clear. A pending merge-async operation refuses later merge-async requests with 409,
  and GitHub keeps it 24 hours with no cancel endpoint. Everything else uses merge-async, because
  the synchronous endpoint merges over rules a bypass-capable credential may skip without asking and
  may not enforce a merge queue (maintainer decision;
  `internal/server/pullapi/routes.go::Handler.synchronousMergeMutatorFor`).
- GitHub reports BLOCKED while required checks are pending, so during `ci=pending`, `blocked`
  does not mean the merge needs a bypass; it may clear once CI passes.
- A merge queue target is exempt from `rules`.

## Merge window and button

Immediate merge, not a queue target
(`frontend/src/lib/components/detail/merge-policy.ts::mergeActionState`):

| ci | rules | Merge button | "Merge after CI" | Immediate merge |
| --- | --- | --- | --- | --- |
| failed | any | existing CI-failed handling | no | existing CI-failed handling |
| pending | any | enabled | offered, no tick needed | `clear`: allowed. `blocked_bypassable`: needs the tick. `blocked`: unavailable. |
| passed | clear | enabled | n/a | allowed |
| passed | blocked_bypassable | enabled | n/a | needs the tick |
| passed | blocked | disabled, with a blocked-by-rules reason | n/a | unavailable |

- Show the bypass checkbox only for `blocked_bypassable`.
- The list menu follows the same table but has no checkbox, so for `blocked_bypassable` with CI
  passed it says the PR needs a bypass and to merge it from the PR page.
- The queued merge button cancels by design, including when merging is blocked or the merge
  operation is unavailable, since cancelling needs no immediate merge; cancellation returns it to
  the normal merge action
  (`frontend/src/lib/components/detail/PullDetail.svelte::mergeButtonPresentation`).
- Cancel turns provider auto-merge off even while Forge's worker also waits, since auto-merge can
  be turned on at the provider beside it. A pull request in the merge queue returns 409 `merge_queued`
  and keeps its workspace cleanup, because the queue still merges it and turning auto-merge off
  does not take it out. A stored read showing it queued stands; otherwise cancel reads the queue
  again after turning auto-merge off, since auto-merge may have enqueued it meanwhile
  (`internal/server/pullapi/deferred_merge.go::Handler.cancelDeferredMergePR`). Cancel never
  drops a kept workspace deletion: it is the user's durable choice, so a merge that happens anyway
  still deletes the workspace.
- The in-queue merge button removes the pull request from the merge queue after a confirmation,
  where the provider has `merge_queue_mutation`. Removal turns provider auto-merge off first, since
  auto-merge left on may enqueue it again, stops a waiting worker, clears the stored queue entry
  once the provider confirms, and keeps a kept workspace deletion
  (`internal/server/pullapi/merge_queue_dequeue.go::Handler.dequeuePR`).

## Provider auto-merge
- Forge chooses the executor before it sends anything: under the per-pull-request merge lock it
  reads the repository's `autoMergeAllowed` and the pull request's auto-merge state, as the write
  credential. A repository that allows auto-merge, or a base branch with a merge queue, which takes
  auto-merge regardless of that setting, gets only the provider's auto-merge (GitHub
  `enablePullRequestAutoMerge`), which outlives a Forge restart and shows on the provider. A
  refusal returns to the caller (`conflict`) and an unknown outcome returns
  `mutationOutcomeUnknown`; neither falls back to Forge's worker, because a worker beside an
  auto-merge the provider accepted would race it. A failed read also fails the request
  (`internal/server/pullapi/deferred_merge.go::Handler.useProviderAutoMerge`).
- Only GraphQL errors that prove nothing ran count as a refusal: `UNPROCESSABLE` (GitHub's answer
  for a merged pull request, observed live), `NOT_FOUND`, and `FORBIDDEN`. Any other error, or one
  without a type, may follow a mutation that ran, so it stays an unknown outcome
  (`platform/github/auto_merge.go::graphQLRefusal`).
- Forge's worker runs when the provider has no auto-merge, neither the repository nor a merge queue
  takes it, or the user consented to bypass rules, which auto-merge cannot do. A worker already
  waiting keeps the merge without a provider read.
- Forge never turns auto-merge on for a pull request in any stack, native or branch-inferred:
  GitHub refuses it for stacked pull requests ("Auto-merge is not supported for stacked pull
  requests"), and with the native stack preview off Forge records a native stack as
  branch-inferred. Such a pull request gets the worker, which waits for the checks and then
  merges or enqueues through merge-async, the only GitHub merge API that takes a stack.
  merge-async has no merge-when-ready action, so the worker's wait stands in for it. Auto-merge
  already on still counts as the deferral
  (`internal/server/pullapi/deferred_merge.go::Handler.enqueueDeferredMerge`).
- A native stack deferral also waits for CI on every unmerged pull request below the target,
  since the stack merge lands them together. Each one's CI is refreshed when the merge is queued,
  and then goes through the target's own check (`deferredMergeCheckState`): the checks the
  provider reports pending on it at queue time must pass, and
  unknown CI fails closed, so one with no CI status is refused up front. It is also refused with
  `stack_member_not_ready` and its number when its CI failed, or it is closed, a draft, or
  conflicting. CI still running below makes a target whose own CI passed deferrable. On every
  pass the worker refreshes the pull requests recorded at queue time, not the current stack
  projection, waits while one still runs, and fails when one fails or stops being mergeable
  (`internal/server/pullapi/deferred_merge.go::Handler.nativeStackWaitsBelow`).
- A native stack going to a merge queue has one exception (maintainer decision): a pull request
  with no CI status when the merge is queued, the target or a member below, has nothing to wait
  for, since GitHub's queue runs the branch's required checks on what it merges. Workflows limited
  to the default branch leave stacked pull requests with no CI. The members with CI still hold the
  merge, CI that starts later counts while it reports, and CI seen when the merge is queued that a
  refresh no longer reports fails closed. A merge that counted on the queue fails instead of merging directly if the queue is gone,
  checked on the same read that picks the mutation
  (`internal/server/pullapi/routes.go::Handler.mergePRWithBodyAdmitted`).
- The lowest unmerged member's base branch decides whether a native stack merge goes to a merge
  queue, for the merge endpoint and the worker alike
  (`internal/server/pullapi/stack_merge.go::mergeQueueOwner`).
- GitHub refuses auto-merge for a pull request it could merge now (`mergeStateStatus` `CLEAN`,
  `HAS_HOOKS`, or `UNSTABLE`, as when only optional checks are pending), so for that state Forge's
  worker waits for the pending checks
  (`platform/github/auto_merge.go::Client.PullRequestAutoMergeState`).
- Auto-merge already on is a success for a plain deferral, so a retry after a lost answer records it
  and keeps a requested workspace deletion; a deferral with bypass consent gets `already_pending`.
  The read runs before the CI checks, so that retry works after CI has passed or failed.
- On a merge-queue branch GitHub's auto-merge enqueues once the checks pass and ignores the
  method, title, and message, as `gh pr merge --auto` does.
- Auto-merge waits on every requirement GitHub enforces, reviews included, and survives new pushes
  by writers; the worker's target-unchanged and CI-only rules do not apply to it.
- Sync stores the provider's auto-merge state as `auto_merge_enabled`; an open pull request with it
  set reports `deferred_merge_pending`. Forge sets it on acceptance so the next detail read shows it
  before sync confirms.
- A workspace deletion requested with it is the user's durable choice, kept like a merge-queue
  enqueue's: it stands until sync records the pull request merged (the workspace is deleted) or
  closed (the write that stores the close drops it, so a reopen cannot revive it), or a later merge
  or deferral replaces it with its own choice (a worker deferral always does, since the worker
  carries its own). Cancelling or re-enabling auto-merge and entering or
  leaving the queue do not touch it (maintainer decision; reasoning about when a cancel makes the
  merge impossible kept producing ordering bugs)
  (`internal/db/queries_merge_workspace_cleanups.go::scanObservedMergeWorkspaceCleanup`).
- With the repository setting off, Forge relies on the stored merge queue state to decide whether a
  queue takes auto-merge, and reads it first when none is stored, as the worker's rules check does
  (`internal/server/pullapi/deferred_merge.go::Handler.useProviderAutoMerge`).
- An immediate merge or enqueue replaces a kept deletion with its own choice before its post-merge
  sync, so merging without "delete workspace" keeps the workspace
  (`internal/server/pullapi/routes.go::Handler.mergePRWithBodyAdmitted`). The cleanup pass deletes
  a merged pull request's workspace only under that pull request's merge lock, and only if the
  request still stands, so a pass that listed it before such a merge finished does nothing
  (`internal/server/pullapi/workspace_cleanup.go::Handler.deleteMergedWorkspace`).
- The provider reports its own outcome through sync; Forge publishes no `deferred_merge_completed`.

## Deferred merge worker

State machine (`internal/server/pullapi/deferred_merge.go::deferredMergeNextStep`):

- **WaitingForCI**
  - CI fails: Failed("a current CI check failed").
  - CI passes: go to Recheck.
- **Recheck.** Keep the target-unchanged, closed, merged-elsewhere, and superseded checks.
  Then read `rules` as the merge credential.
  - `queue`: enqueue.
  - `clear`: Merge.
  - Blocked (either kind) with `consent`: Merge. This is the only path that merges over the rules.
  - Blocked (either kind) without `consent`: Failed("merge needs a bypass of branch protection or
    repository rules; it was not performed"). Do not keep waiting.
  - `unknown`: retry on the next tick within the existing wait budget. When the budget runs out,
    fail with a message saying Forge could not confirm the merge state.
- **Merge:** succeeds as Merged, or reports the provider's refusal as Failed.

- Recheck reads `rules` fresh as the merge credential and never trusts the sync credential's read
  alone, which under split auth reports BLOCKED forever. A host that can never answer keeps the
  provider's state (`internal/server/pullapi/deferred_merge.go::Handler.deferredMergeRules`).
- Queued deferred merges live only in the server process; a restart drops them. Detail
  responses expose the state as `deferred_merge_pending`.
- Queueing a second worker returns 409 `already_pending`, so the UI must not offer
  deferred actions while `deferred_merge_pending` is true.
- Merge and close controls follow GitHub's semantics on every provider: Close stays enabled
  while a deferred merge waits and while the pull request is in a merge queue, and closing
  ends a queued deferred merge at the close, as GitHub disables auto-merge. Every accepted
  closed snapshot, from Forge's close or from sync, stops the worker and publishes its
  failure; a reopen does not restore it
  (`internal/server/pullapi/deferred_merge.go::Handler.StopDeferredMergeForTerminalPull`).

## Completion and supersession

- Pending must be cleared **before** broadcasting a terminal `deferred_merge_completed`
  (success or failure); clients refresh detail on the event and that read must not report a
  queued merge.
- A successful immediate merge or merge queue enqueue supersedes the queued worker silently:
  pending clears with the merge response and the worker emits no event. An immediate response
  with `merged: false` leaves the queued merge and all merge-completion side effects untouched.
- Immediate and deferred merges and enqueues hold a per-pull-request merge lock through the
  provider call, and the worker checks supersession under it, because an enqueue leaves the PR
  open where target checks cannot see it (`internal/server/pullapi/routes.go::Handler.mergePRWithBodyAdmitted`).
- A deferred merge that went to the merge queue completes as `queued`: clear pending, publish no
  merged status, and leave the outcome to the PR's queue state; its workspace deletion is kept
  like an immediate enqueue's (`internal/server/pullapi/deferred_merge.go::Handler.completeDeferredMerge`).
- A deferred `merged: false` response is a terminal failure: clear pending and publish the
  provider message, but never publish merged status or run merge-completion side effects.
- The worker stands down silently whenever it sees the target already merged, since it can see
  the merge before the immediate path supersedes it. A closed (not merged) target still
  broadcasts a failure.
- In-flight cleanup is compare-and-delete on the per-key handle, so a stale worker's cleanup
  must not remove a newer queue's handle for the same key.

## Fleet and workspaces

- Spoke preparation rejects new deferred-merge admission and counts every admitted handle
  until it reaches a known terminal outcome; the acknowledgement generation cannot freeze while
  any remain (`internal/server/pullapi/deferred_merge.go::deferredMergeHandle.finish`).
- Spokes never construct local provider or deferred-merge workers. Active spokes proxy deferred
  merges through a validated hub client; inactive or incompatible spokes reject them without
  federation egress (`internal/server/server.go::Server.serveProviderRoute`).
- Deferred merges retain the workspace ID and authenticated originating spoke before background
  execution; only success admits cleanup on the owning daemon
  (`internal/server/pullapi/deferred_merge.go::Handler.enqueueDeferredMerge`).
- Successful completion can carry `workspace_cleanup_pending`; cleanup completion is reported
  by workspace lifecycle events, so the merge event never infers deletion (`internal/server/pullapi/deferred_merge.go::DeferredMergeCompletedPayload`).
- Queue acknowledgement refreshes deferred pending state; it must never imply provider merge
  completion or workspace deletion (`frontend/src/lib/stores/detail.svelte.ts::MergePullOutcome`).
