# Deferred ("merge after CI") merge invariants

Use this document for changes to deferred merge queueing, cancellation,
supersession, completion events, or pending-state presentation.

## Merge rules

- Stack position alone does not warrant a merge warning; gh-stack users may queue merges at
  any position, so show neutral stack context rather than cautioning about merge order.
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
- With a merge already queued, a PR whose immediate merge is unavailable keeps the button
  disabled, since the modal then offers only an immediate merge.

## Provider auto-merge

- A deferred merge goes to the provider's own auto-merge (GitHub `enablePullRequestAutoMerge`)
  when the provider has one, so it outlives a Forge restart and shows on the provider. When the
  provider certainly did not enable it (`ErrCodeConflict`), for example because the repository
  disallows auto-merge or the pull request can already merge, Forge's worker takes over; Forge
  never pre-reads `autoMergeAllowed`. A lost answer fails the request instead, since a worker
  could then merge alongside an auto-merge GitHub accepted. A waiting worker keeps the merge, and
  the provider is not asked
  (`internal/server/pullapi/deferred_merge.go::Handler.enableProviderAutoMerge`).
- A deferred merge with bypass consent always uses Forge's worker: auto-merge cannot merge over
  the rules.
- On a merge-queue branch GitHub's auto-merge enqueues once the checks pass and ignores the
  method, title, and message, as `gh pr merge --auto` does.
- Auto-merge waits on every requirement GitHub enforces, reviews included, and survives new pushes
  by writers; the worker's target-unchanged and CI-only rules do not apply to it.
- Sync stores the provider's auto-merge state as `auto_merge_enabled`; an open pull request with it
  set reports `deferred_merge_pending` and rejects a second deferred merge as `already_pending`.
  Forge sets it on acceptance so the next detail read shows it before sync confirms.
- A workspace deletion requested with it is kept like a merge-queue enqueue's, and a queue read that
  finds the pull request not yet queued does not drop it while auto-merge is still enabled
  (`internal/db/queries_merge_workspace_cleanups.go::scanObservedMergeWorkspaceCleanup`).
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
- Closing the pull request is the user's only cancel for a queued deferred merge; queueing a
  second one returns 409 `already_pending`, so the UI must not offer deferred actions while
  `deferred_merge_pending` is true.
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
  broadcasts a failure; closing is the user's cancel.
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
