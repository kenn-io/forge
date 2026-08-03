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

The information being reconstructed already exists exactly in the sync path:
every complete sync fetches the provider's current commit list for the merge
request, which is precisely the set of commits reachable from the current
head. A stored commit event whose SHA is absent from that list is obsolete; a
present one is live. No inference is required.

## Design

### Sync stamping

A helper beside `commitOrderAssigner` in `internal/github/sync.go` takes the
stored MR events (already loaded by every commit-syncing path) and the current
provider commit SHA set, and returns updated copies of stored commit events
whose `obsolete` metadata flag must change:

- SHA absent from the current list: set `obsolete: true` in the event's
  metadata JSON, beside the existing `commit_order_key`.
- SHA present in the current list: remove the flag. Restoration is symmetric
  and recomputed on every complete sync, so any push pattern converges to the
  provider's truth.

Updated copies join the same `UpsertMREvents` batch as the freshly synced
events. The existing conflict path already refreshes `metadata_json`, and the
batch commits inside the same revision-guarded dataset transaction, so
concurrency and epoch safety are inherited unchanged.

Stamping requires a known-complete current commit list. The bulk GraphQL path
gates on its commits-complete flag; the other paths stamp only where the
client returns the full list, verified per path during implementation. An
incomplete round skips stamping entirely: flags may go stale, never wrong.
The generic provider extras path is shared, so GitLab, Forgejo, and Gitea
inherit stamping the same way they inherit `commit_order_key`.

### Frontend collapse

The lineage replay machinery in `EventTimeline.svelte` (`obsoleteCommitOrders`
and its lineage/obsolete-set state) is deleted. Strict-date collapse selects
commit events whose parsed metadata has `obsolete === true`. The force-push
boundary and generation code remains for grouped-mode ordering; only the
which-commits-are-obsolete decision moves to the backend.

### Data lifecycle

An absent flag means not collapsed. Live merge requests backfill on their next
complete sync because flags are recomputed each round. Merge requests that
never sync again do not collapse; there is no frontend fallback to the old
heuristic.

## Tests

- Go sync tests through real SQLite: replace a lineage and assert flags set on
  the removed commits; push back and assert flags cleared and set on the
  displaced replacement (alternation); restore a subset and assert only absent
  SHAs stay flagged; an incomplete commit list skips stamping and leaves prior
  flags untouched.
- Component test: flagged commits collapse in strict date order and unflagged
  commits render normally, with no replay scenarios.
- Full stack: the SQLite-backed browser fixture seeds `obsolete` metadata on a
  replaced lineage, and the Playwright timeline case verifies the flag
  survives the database and detail API into strict-date rendering.

## Verification

Run the affected Go package tests, the full frontend Vitest suite, the
affected full-stack Playwright suite, and the repository's small-change
verification checks for cross-layer metadata changes.
