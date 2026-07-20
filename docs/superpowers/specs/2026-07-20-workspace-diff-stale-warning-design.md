# Workspace Diff Stale Warning Design

## Problem

Workspace diff cache entries currently set the shared `stale` response flag
when their validation timestamp is more than 15 seconds old. The shared diff
view interprets that flag as proof that it is showing an earlier Git revision,
so a normal stale-while-revalidate cache hit can display a warning even when
the workspace has not changed.

## Decision

For workspace diffs, cache age remains an internal revalidation concern and
must not set the user-visible `stale` flag. A cached workspace diff is stale
only when its recorded Git `HeadOID` differs from the workspace's currently
resolved `HeadOID`.

The cache will resolve the current workspace snapshot specification on a cache
hit, compare its head with the cached snapshot head, and set `stale` from that
comparison. It will continue to schedule age-based background fingerprint
validation exactly as before. If the current head cannot be resolved, the
last-known-good snapshot is served without the warning; an unknown state is
not a confirmed mismatch.

Pull-request diff staleness remains unchanged because it is computed from the
persisted platform and diff SHAs outside the workspace cache.

## Verification

Focused cache tests will prove that:

- an old cache entry whose recorded head matches the current Git HEAD is not
  marked stale;
- a cache entry whose recorded head differs from the current Git HEAD is
  marked stale;
- age-based background validation is still requested for old entries.

Existing server and UI tests cover propagation of the shared `stale` flag and
the banner rendering, so no new browser-only duplicate is required.
