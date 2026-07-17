# Selected workspace diff loading

## Goal

Make workspace diff views feel immediate after a workspace is selected, while
also fixing the cold path so a cache miss is proportional to one aggregate Git
diff rather than to the number of changed files.

The live baseline for workspace `539fb58f99084088` was:

- HEAD: `/files` 1.38s followed by `/diff` 1.90s;
- merge target: `/files` 8.84s followed by `/diff` 16.13s;
- merge-target response: 128 files and about 4 MB of JSON;
- the aggregate Git patch command itself: about 240ms.

The dominant avoidable cost is workspace whitespace-only classification, which
currently invokes `git diff --numstat -w` once per changed path. The frontend
then pays another independent Git walk because it requests `/files` and `/diff`
sequentially.

## Scope

This change covers local workspace diff files, patches, and their selected-view
refresh lifecycle. It retains the current workspace bases, commit/range scopes,
file previews, response shapes, generated-file attributes, untracked-file
handling, copy/rename detection, path safety, and explicit hide-whitespace
behavior.

Provider pull-request diffs and repository-browser diffs are not cache clients
in this change. Fleet-proxied workspaces receive the cold-path improvement when
the member computes their local diff; proactive selection refresh remains owned
by the server that owns the worktree.

## Git and Go responsibilities

Git remains authoritative for:

- base, upstream, and merge-base resolution;
- the ordinary aggregate raw metadata, numstat, and patch;
- rename/copy detection, including `--find-copies-harder`;
- binary patch decisions and `check-attr` generated-file metadata;
- the explicit hide-whitespace patch, where `-w` can change hunk alignment in a
  file that contains both whitespace and substantive edits.

Go owns whitespace-only classification for the ordinary diff. This is a small
port of Git xdiff's `XDF_IGNORE_WHITESPACE` record equivalence, not a new diff
engine. Git's current implementation splits input into line records, ignores C
`isspace` bytes while hashing and comparing each record, and preserves record
count and order. The Go equivalent uses the ASCII whitespace set recognized by
Git in the C locale (`space`, tab, newline, vertical tab, form feed, carriage
return), preserves line boundaries, and ignores final-newline differences in
the same way.

For every modified non-binary file, the classifier reconstructs the old and new
line sequences represented by each ordinary patch hunk. A hunk is
whitespace-only only when its old and new record counts match and corresponding
records match after Git-compatible whitespace removal. A file is
whitespace-only only when every changed hunk is whitespace-only. Added,
deleted, renamed, copied, type-changed, and binary files are never classified
as whitespace-only, matching the current `--no-renames -w` classification
contract.

Git remains the test oracle. Table-driven and generated parity cases compare
the Go classifier with `git diff --quiet -w` for indentation, tabs, blank-line
insertion, CRLF, missing final newlines, repeated lines, mixed substantive and
whitespace edits, binary files, renames, and copies. If ordinary hunks cannot
reproduce Git parity for an edge case, the implementation must use complete
old/new record sequences for that file rather than weaken the contract or add
per-file Git calls.

## Shared workspace snapshot

A workspace diff snapshot contains the complete `gitclone.DiffResult` plus the
file-list projection derived from it. `/files` and `/diff` use the same
preparation and concurrent requests for the same key share one in-flight
computation. `/files` never starts a second raw/numstat/whitespace scan after a
full snapshot has been prepared.

The key includes:

- workspace ID and normalized worktree path;
- base (`head`, `pushed`, or `merge-target`) and resolved base identity;
- commit/range scope;
- ordinary or explicit hide-whitespace mode.

Path-scoped diff reads select the requested file from a prepared whole-diff
snapshot when the matching snapshot exists. File-content previews remain
separate bounded reads and are not stored in the snapshot cache.

Preparation records trace phases for base resolution, revision validation, Git
raw metadata, numstat, patch generation, Go whitespace classification,
untracked-file loading, generated-attribute lookup, and response assembly. The
request span records cache result (`hit`, `stale`, `miss`, or `coalesced`) and
snapshot size so the live trace shows whether latency is Git, Go processing,
serialization, or cache waiting.

## Selection, validation, and refresh

Only a workspace selected in a terminal view receives proactive preparation or
background validation. Workspace list rows and inactive workspaces do not start
diff work.

For a local workspace, the terminal view's existing dedicated SSE connection
carries `workspace_id`. Its lifetime is the selection lease. The server
reference-counts concurrent tabs, prepares the default HEAD snapshot on the
first selection, and stops proactive work when the final selection disconnects.
Previously prepared entries may remain available until eviction, but inactive
entries are not refreshed. Fleet selections keep request-driven caching and the
cold-path improvement; they do not create a proactive lease on the remote
member in this change.

The last requested base/scope/whitespace key becomes the selected workspace's
active snapshot. Changing scope prepares that key; older keys remain read-only
cache entries until eviction.

Validation is cheaper than recomputation. A revision fingerprint includes the
resolved Git refs and the selected worktree's changed-path state. Unchanged
metadata extends freshness without running the aggregate patch. Changed
metadata is confirmed from changed file contents so edits that retain the same
add/delete totals are not missed. Fingerprints taken before and after
preparation must match before the result is published; a concurrent edit leaves
the previous snapshot in place and schedules another debounced validation.

The existing worktree-stats change signal requests prompt validation for the
selected worktree. A bounded selected-workspace validation interval is the
fallback for changes that do not alter aggregate stats. Concurrent validation
or preparation for one key is single-flight.

When validation finds the same fingerprint, it performs no diff recomputation
and emits no event. When a stable recomputation produces a changed snapshot,
the server atomically replaces the entry and broadcasts
`workspace_diff_changed` with workspace/host identity and snapshot revision.
The terminal filters that event to its selected workspace and reloads only the
currently visible diff scope. The stale snapshot remains visible until the
replacement request completes.

## Cache bounds and failures

Snapshots use an in-memory TTL cache bounded by both inactivity and approximate
serialized bytes. Eviction removes least-recently-used inactive entries before
selected entries. Cache loss or eviction is never an API failure; the next read
uses the cold path.

Preparation errors do not replace a last-known-good snapshot and do not emit a
change event. A cold request with no usable snapshot preserves the current API
problem response. A stale response is allowed only when a last-known-good
snapshot exists; background failure is recorded on the preparation span and a
later validation retries. Server shutdown cancels validators and in-flight
preparation through the existing background lifecycle.

## Frontend behavior

The selected local terminal workspace identifies itself on the scoped SSE
connection. `workspace_diff_changed` increments a diff-specific refresh token
only for the matching workspace. The diff store retains its current files and
patch during background replacement instead of clearing them to a loading
screen.

The current progressive file-list presentation remains: `/files` populates the
sidebar before the frontend starts its `/diff` request. The cold `/files`
preparation completes the shared snapshot, so the following `/diff` is a cache
projection and performs no Git work.

## Verification

- Go unit tests pin the xdiff-compatible whitespace record comparison and
  file-classification rules against Git oracle cases.
- Workspace diff tests prove `/files` and `/diff` share preparation, concurrent
  requests coalesce, explicit hide-whitespace retains Git hunk semantics, and
  path/preview behavior remains safe.
- Cache tests use a fake clock and preparer to prove selected-only prewarming,
  unchanged validation without recomputation, stable changed replacement,
  single-flight behavior, byte/TTL eviction, disconnect handling, and
  last-known-good behavior after failure.
- Wire-level SSE tests prove selection registration and matching
  `workspace_diff_changed` delivery.
- Frontend tests prove workspace-qualified event filtering and stale-visible
  refresh.
- Before/after live traces record cold and warm HEAD/merge-target loads for the
  profiled workspace. The acceptance condition is that the cold path has no
  per-file Git subprocess growth and warm requests perform no Git diff work;
  any remaining multi-second cold phase is investigated rather than hidden by
  the cache.
