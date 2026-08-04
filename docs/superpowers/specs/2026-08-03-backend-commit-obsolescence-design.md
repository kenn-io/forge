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

### Sync stamping

A helper on the syncer stamps stored commit events from local-clone ancestry.
It runs where diff sync has just resolved the merge request's current head
against the clone, and it is guarded by the clone containing that head: the
head's ancestor closure is then complete, so a stored commit SHA that is not
an ancestor of the head (`git merge-base --is-ancestor` via the existing
`gitclone.Manager` primitives) is genuinely unreachable, including SHAs absent
from the clone entirely.

- SHA not an ancestor of the current head: set `obsolete: true` in the
  event's metadata JSON, beside the existing `commit_order_key`.
- SHA an ancestor of the current head: remove the flag. Stamping is
  recomputed from scratch on every round, so any push pattern (alternation,
  partial restore, split ancestry) and any base movement converges to the
  git-verified truth.

Updated copies of changed events join the same `UpsertMREvents` batch as the
freshly synced events. The existing conflict path already refreshes
`metadata_json`, and the batch commits inside the same revision-guarded
dataset transaction, so concurrency and epoch safety are inherited unchanged.

When the clone is unavailable or does not contain the current head, stamping
skips and flags keep the state of the last verified round — the same soft
dependency diff sync already has. A skipped round can leave a restored commit
collapsed (or a removed one visible) until the next verified round; flags
reflect the last verified sync, not live provider state. Ancestry stamping is
provider-agnostic: any provider whose merge requests get diff sync inherits
it, with GitHub wired first alongside the existing `commit_order_key`
stamping.

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

- Go stamping tests through real SQLite and a real git clone fixture: replace
  a lineage and assert flags set on the removed commits; push back and assert
  flags cleared and set on the displaced replacement (alternation); restore a
  subset and assert only unreachable SHAs stay flagged; advance or retarget
  the base without a force push and assert nothing is stamped obsolete; a
  clone missing the current head skips stamping and leaves prior flags
  untouched.
- Component test: flagged commits collapse in strict date order and unflagged
  commits render normally, with no replay scenarios.
- Full stack, real computation: an integration test drives the real sync path
  with successive head states and asserts the stamped flags surface through
  the detail API, so the test fails if stamping is missing or unwired rather
  than only proving metadata propagation.
- Full stack, browser boundary: the SQLite-backed fixture seeds `obsolete`
  metadata on a replaced lineage, and the Playwright timeline case verifies
  the flag survives the database and detail API into strict-date rendering.

## Verification

Run the affected Go package tests, the full frontend Vitest suite, the
affected full-stack Playwright suite, and the repository's small-change
verification checks for cross-layer metadata changes.
