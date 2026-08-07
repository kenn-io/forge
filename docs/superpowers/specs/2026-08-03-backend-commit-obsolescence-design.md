# Backend-Authoritative Commit Obsolescence Design

Supersedes `2026-08-03-force-push-lineage-restoration-design.md` and the plan
`docs/superpowers/plans/2026-08-03-force-push-lineage-restoration.md`, which
are removed with this design.

## Problem

Strict date order collapses commits that force pushes removed from the branch.
The current implementation reconstructs that set in the frontend by replaying
`force_push` events (`before_sha`/`after_sha` pairs) over stable
`commit_order_key` values and inferring lineage membership with interval
heuristics. A force-push event names two heads, not two commit sets, so the
ancestor set of a replacement head is not derivable from the event stream.
Five consecutive review rounds each found a push pattern the heuristic could
not express (permanent union, head-only restore, alternation, partial restore,
split ancestry), and further counterexamples remain constructible, such as
rebases that reuse a subset of commits under new SHAs.

The question the UI asks is whether a stored commit is still reachable from
the merge request's current head. The provider's commit list cannot answer it:
that list is the base-to-head comparison, so base advancement or retargeting
removes commits from the list while they remain reachable from the head, and a
list-diff design would wrongly collapse live commits (roborev jobs 9625 and
9626). Diff sync, however, already maintains a local bare clone and fetches
the current head, and git answers reachability exactly.

## Design

### Liveness rides the snapshot transaction

Repeated review rounds each found another race between stamping and the
sync's own writes (stale caches, clobbered metadata, ordering shuffles,
error-path invalidation). The root cause was structural: stamping was a
second, independent writer beside the sync's revision-guarded snapshot
machinery. This design removes the second writer.

Liveness is computed before a round commits and its results travel with the
round's own dataset:

- A commit event's SHA (from `PlatformExternalID` when it is a full SHA,
  else the summary) is live iff it is reachable from the round's verified
  head. Reachability is answered in-process with go-git against the local
  bare clone — open the bare repository, resolve the head commit (a missing
  head means the round commits without liveness updates), and walk ancestry
  from the head with early termination once every candidate is resolved.
  The walk reads only what it needs — parent hashes — and refuses
  contributor-controlled bulk: each commit's encoded size is checked before
  go-git decodes it (1 MB cap; real commit objects are a few hundred
  bytes), a discovery budget (50k distinct commits, counted at enqueue so
  the frontier itself stays bounded) limits the traversal, and a total
  parent-edge budget (500k) refuses graphs whose repeated-parent fan-out no
  real merge history approaches. Exceeding any limit reports the head
  unverifiable instead of returning partial verdicts.
  Read-only, lock-free, no subprocesses; the git CLI remains only for
  networked operations (clone/fetch with credentials). go-git is an
  explicitly maintainer-approved dependency for this. SHA-256 (64-hex)
  repositories are an explicit non-goal: their candidates are never
  evaluated and never flagged.
- Incoming commit events carry the computed `obsolete` flag in their
  metadata within the normal upsert batch. Stored commit events the provider
  did not re-list receive metadata-only updates applied inside the same
  revision-guarded snapshot transaction that commits the round (the child
  snapshot payload carries them). A round that loses the revision CAS writes
  nothing — events and flags stay mutually consistent by construction, and
  stale rounds are inert.
- Unchanged/not-modified rounds apply the same rule through their
  revision-guarded detail-fetched marker write, so a round whose clone has
  since caught up repairs flags without any dedicated retry machinery.

The stamping mutex, MR-row head recheck, and cache-invalidation rules are
deleted; serialization is the snapshot guard's job. The only cache is a
per-MR memo of the reachability answer keyed by a hash of the walk's exact
inputs — the head SHA plus the sorted candidate set. Reachability over
immutable git history is a pure function of that key, so the memo needs no
invalidation and cannot be corrupted by concurrent rounds: a hit replaces
only the ancestry walk and its verdicts flow through the identical flag
injection as a fresh computation, so a hit can never skip a needed write.
Only verified walks are memoized. The memo holds provider-controlled input
and is bounded on both axes: at most 1024 MR entries with least-recently-used
eviction, and candidate sets larger than 4096 are computed normally every
round but never memoized. Neither bound affects flag correctness — a miss or
an unmemoized round only costs one budget-capped walk.

When the clone is unavailable or lacks the round's head, the round commits
its events without liveness updates: provider-listed commits still receive
fresh metadata (the provider listing a commit is itself liveness evidence,
and showing a commit wrongly is the safe direction), while unlisted events
keep their last verified flags. Ancestry liveness is provider-agnostic: any
provider whose merge requests sync through the shared flows inherits it.

Liveness runs while an MR is open and once more on the round that takes it
out of the open state, computed against the final head — the flags that
round persists are the terminal record. Because no later round will ever
recompute them, finalization is intrinsic to the parent-snapshot choke
point: the snapshot transaction itself detects the open-to-terminal
transition (prior stored state open, incoming state not), reads the stored
events inside the transaction, runs the liveness computation there, and
lands the flags with the terminal state — together or not at all, computed
from data no concurrent round can shift. No caller can commit a terminal
transition without it. Every state transition flows through this one path,
and UI mutations never write local state eagerly (an eager write would
suppress the transition and push updated_at past the provider's): merge
re-reads the provider through the same close-detection flow the periodic
sync uses, because a merge result is not an MR snapshot, while close and
reopen commit the merge request the state mutator itself returned —
provider adapters must return the complete updated MR with authoritative
timestamps from an edit.
Already-merged and already-closed MRs are never refetched or recomputed; a
reopened MR computes again like any open one.

### Frontend collapse

The lineage replay machinery in `EventTimeline.svelte` (`obsoleteCommitOrders`
and its lineage/obsolete-set state) is deleted. Strict-date collapse selects
commit events whose parsed metadata has `obsolete === true`. The force-push
boundary and generation code remains for grouped-mode ordering; only the
which-commits-are-obsolete decision moves to the backend.

### Data lifecycle

An absent flag means not collapsed. Live merge requests backfill on their next
verified round because flags are recomputed each time. Merge requests that
never sync again, or whose diff sync is unavailable, do not collapse; there is
no frontend fallback to the old heuristic.

## Tests

- Go liveness tests through real SQLite and a real git clone fixture: replace
  a lineage and assert flags set on the removed commits; push back and assert
  flags cleared and set on the displaced replacement (alternation); restore a
  subset and assert only unreachable SHAs stay flagged; advance or retarget
  the base without a force push and assert nothing is stamped obsolete; a
  clone missing the current head commits the round without liveness updates
  and unlisted events keep prior flags; a round that loses the revision CAS
  writes neither events nor flags (staleness by construction, replacing the
  old interleaving regressions); go-git reachability answers match real
  repository history including candidates absent from the object store.
- Component test: flagged commits collapse in strict date order and unflagged
  commits render normally, with no replay scenarios.
- Full stack, real computation: an integration test drives the real sync path
  with successive head states and asserts the stamped flags surface through
  the detail API, so the test fails if stamping is missing or unwired rather
  than only proving metadata propagation. The same harness closes the merge
  request through the UI state route after a force push and asserts the
  terminal record is computed against the final head and never recomputed by
  later syncs.
- Full stack, browser boundary: the SQLite-backed fixture seeds `obsolete`
  metadata on a replaced lineage, and the Playwright timeline case verifies
  the flag survives the database and detail API into strict-date rendering.

## Verification

Run the affected Go package tests, the full frontend Vitest suite, the
affected full-stack Playwright suite, and the repository's small-change
verification checks for cross-layer metadata changes.
