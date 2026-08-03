# Hot PR View Tracking Design

## Problem

The fast merge-request sync lane currently treats provider activity within 30 minutes as hot. That spends API calls on recently changed PRs even when the user is not looking at them, while PRs the user repeatedly revisits can fall out of the fast lane. The immediate watched-MR entry point can also overlap the scheduled pass and race on `nextWatchSyncAfter`.

## Decisions

- A hot PR is one of the last 10 unique open PR details successfully viewed through the detail API.
- Hot membership is persisted in SQLite and survives daemon restarts.
- A successful detail GET moves an open PR to the front of the hot set. Failed and not-found requests do not alter it.
- The hot set is refreshed on the existing configured watched-PR cadence, whose default remains two minutes.
- Other open PRs with effective activity inside `active_pr_window` are warm. Effective activity remains the later of provider activity and linked notification activity. Warm details are due every 10 minutes.
- Explicit workspace-linked watched PRs retain the configured watched-PR cadence independently of hot membership.
- A PR is never inserted into the hot set unless its persisted state is open.
- Persisting a merged or closed state removes the PR from the hot table immediately. Deleted PR rows cascade out as well. Candidate selection still filters for open state defensively.
- Terminal eviction may leave fewer than 10 hot rows. Previously displaced rows are not resurrected.
- Scheduled and immediate watched-MR passes are serialized for the full pass with a dedicated mutex. This protects `nextWatchSyncAfter` and avoids duplicate provider work.

## Storage

Migration `000046` adds `forge_hot_merge_requests`:

- `merge_request_id INTEGER PRIMARY KEY` referencing `forge_merge_requests(id)` with `ON DELETE CASCADE`
- `viewed_at DATETIME NOT NULL`
- an index ordered by `viewed_at` for MRU reads and trimming
- a trigger that deletes a hot row after the associated merge request changes to `closed` or `merged`

`RecordHotMergeRequestView` performs an `INSERT ... SELECT` constrained to an open merge request, upserts `viewed_at`, and trims all but the 10 newest rows in one transaction. `ListHotMergeRequestIDs` returns open IDs in most-recent-first order.

## Request and Scheduler Flow

After the detail response has been built successfully, `GET /pulls/{number}` records the view. Failure to record the local freshness hint is logged but does not turn an otherwise valid detail response into an HTTP error.

The watched-MR candidate builder unions three sources:

1. explicit workspace-linked watched PRs, always due on the configured watched cadence;
2. persisted hot PRs, due when never fetched or when their detail is at least the configured watched interval old;
3. warm activity/notification candidates, due when never fetched or when their detail is at least 10 minutes old.

The existing provider routing, repository tracking, rate-limit checks, ETag behavior, and host-level cadence gates remain unchanged.

## Verification

- Migration/query tests prove MRU ordering, uniqueness, trimming to 10, persistence, refusal of terminal PRs, and automatic terminal eviction.
- Pull API coverage proves that only a successful open-PR detail response records a view.
- Sync selection tests distinguish configured-cadence hot PRs from 10-minute warm candidates and retain notification activity as a warm scheduling hint.
- A concurrency regression test blocks one provider fetch, starts a second immediate watched pass, and proves only one pass enters provider work at a time.
