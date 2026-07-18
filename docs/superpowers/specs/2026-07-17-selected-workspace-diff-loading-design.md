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
in this change. A fleet long-poll holds the selection lease on the member that
owns the worktree; the hub relays only opaque HEAD versions and never performs
the member's Git work.

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
separate bounded reads and are not stored in the snapshot cache. A new-side
worktree preview is intentionally live and can move ahead of the cached patch;
blob-backed old/range sides remain pinned to resolved OIDs.

Preparation records trace phases for base resolution, revision validation, Git
raw metadata, numstat, patch generation, Go whitespace classification,
untracked-file loading, generated-attribute lookup, and response assembly. The
request span records cache result (`hit`, `stale`, `miss`, or `coalesced`) and
snapshot size so the live trace shows whether latency is Git, Go processing,
serialization, or cache waiting. Both projections carry an opaque cache-generation
plus revision token. `/diff` can require the revision returned by `/files`; a
mismatch asks the client to restart the pair rather than combining revisions.

## Selection, validation, and refresh

Only a workspace selected in a terminal view receives proactive preparation or
background validation. Workspace list rows and inactive workspaces do not start
diff work.

For a local workspace, the terminal view's existing dedicated SSE connection
carries `workspace_id`. Its lifetime is the selection lease. The server
reference-counts concurrent tabs, prepares the default HEAD snapshot on the
first selection, and stops proactive work when the final selection disconnects.
Previously prepared entries may remain available until eviction, but inactive
entries are not refreshed. Fleet selections use a 25-second `/diff/watch`
long-poll through HTTP or SSH proxying. Empty or foreign tokens return the
current HEAD version with `changed=true`; matching tokens return that same
version with `changed=false` on timeout. Events only trigger a reread of the
watched HEAD key, so versions never cross diff scopes. Cancellation releases
the remote lease, while recently active scopes survive immediate reconnects.

Every base/scope/whitespace key requested by a currently selected workspace is
active until its access lease ages out. This keeps concurrent tabs with different
diff scopes independent. Active keys are pinned against eviction; older inactive
keys remain read-only cache entries until eviction. Expired active-key records
are pruned even while their workspace remains selected, so a scope that has not
been read for 10 minutes cannot stay pinned forever.

Validation is cheaper than recomputation. Each validation re-resolves the logical
specification, then fingerprints the resolved Git refs and changed-path state.
Unchanged strong stat identities reuse per-file content digests; changed metadata
is confirmed from file contents so same-size edits are not missed without
re-reading unchanged large files every 15 seconds. The fingerprint also includes
the repository-local attribute input (`.git/info/attributes`); the hardened Git
runner already excludes user and system configuration. Commit/range generated
file classification uses the resolved head commit as `git check-attr --source`;
only live worktree snapshots consult worktree `.gitattributes`. Preparation uses resolved
OIDs, then re-resolves and fingerprints again before publication. Equal boundary
fingerprints are best-effort movement detection rather than a filesystem
transaction: an A-to-B-to-A mutation can evade them. A mismatch leaves the
previous snapshot in place and schedules a throttled retry.

The existing worktree-stats change signal requests prompt validation for the
selected worktree. A bounded selected-workspace validation interval is the
fallback for changes that do not alter aggregate stats. Concurrent validation
or preparation for one key is single-flight. One background validation worker
serializes proactive Git preparation and remains occupied until the shared
cache-owned preparation completes, with a 30-second ceiling so one caller cannot
cancel another while its Git work continues. Foreground cold reads do not wait
behind the background queue. If the first selected-workspace prewarm
cannot resolve or prepare, it retries every five seconds while the selection
lease remains open.

All entries become eligible for validation after 15 seconds. This is a freshness
threshold, not a completion SLA when the bounded worker queue is backlogged.
Selected entries validate proactively; an older unselected entry returns its
last-known-good value as stale and schedules request-driven validation, so
frequent fleet reads cannot keep a snapshot fresh indefinitely. A one-second
scheduler begins validation during
the final second of that window so timer phase cannot stretch the bound toward
30 seconds. When validation finds the same fingerprint, it
performs no diff recomputation, does not renew foreground access time, and emits
no event. Manual workspace refresh schedules best-effort validation for every
cached key for that workspace, including unselected and fleet-owned entries; it
is queued before provider refresh and does not wait for preparation. Provider or
preparation failures leave the last-known-good snapshot visible and retry on a
later signal or read. When a stable recomputation produces
a changed snapshot,
the server atomically replaces the entry and broadcasts
`workspace_diff_changed` with workspace/host identity and snapshot revision.
The terminal filters that event to its selected workspace and reloads only the
currently visible diff scope. The stale snapshot remains visible until the
replacement request completes. Versions are opaque equality tokens; clients do
not infer ordering from version or revision values because SSE event IDs own
ordering and replay. Revision identity includes a per-process cache generation,
and `reconnect.stale` always triggers a preserving diff refresh, so restart,
eviction, and replay-ring loss cannot strand an old display.

## Cache bounds and failures

Snapshots use `jellydator/ttlcache/v3` for entry storage and TTL expiration.
Middleman's coordinator applies the 128 MiB inactive-entry target so pressure
cannot evict selected or pair-retained snapshots, and retains ownership of
coherent projections, stable publication, and single-flight preparation.
New snapshots receive a one-minute pair-retention lease so an oversized
`/files` projection survives long enough for its revision-pinned `/diff` read.
The target may therefore be exceeded temporarily by pair-retained snapshots or
the active working set. Active protection expires after 10 minutes without
foreground access, at which point normal cost eviction can recover memory.
Cache loss or eviction is never an API failure; the next read uses the cold path.
Mixed-version fleet members are not given a compatibility fallback for revision
pinning: hub and member must run a version that supports snapshot versions and
typed `snapshot_changed` conflicts.

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

Workspace switching gives the shell/runtime critical-path priority over diff
work. As soon as route identity differs from the loaded workspace, the old
right sidebar is unmounted, its identity-scoped files/diff requests are aborted,
and a neutral sidebar placeholder replaces it. The new sidebar mounts after
workspace metadata and either matching runtime state or a terminal runtime error
belong to the selected route, so runtime failure cannot strand the placeholder.
A monotonic load generation is checked after every await and rejection path.
Each invocation also has a token, so cleanup from an older same-workspace load
cannot abort its replacement. Same-workspace background snapshot replacement
continues to preserve the visible diff. A typed `snapshot_changed` file-preview
conflict reloads the coherent files/diff pair once and retries the preview.

This browser-side deferral does not defer server preparation. The scoped SSE
selection lease starts default-HEAD prewarming immediately alongside workspace
and runtime reads. The event subscriber is registered before that lease starts,
so even immediate prewarm completion reaches the selecting client. Initial selected prewarm publication emits
`workspace_diff_ready` with the snapshot version; the client records it without
mounting the diff panel, then uses it to request the already-prepared snapshot
after runtime readiness. Preparation failure emits no ready event and never
delays or fails the shell.

Fleet selection starts its watch at route selection, independently of metadata,
runtime, and sidebar mounting. The watch keeps its own HEAD token, aborts through
the proxy on switch, and retries failures with capped exponential jitter; only a
changed HEAD token triggers a preserving diff reload.

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
  `workspace_diff_ready`/`workspace_diff_changed` delivery.
- Frontend tests prove workspace-qualified event filtering and stale-visible
  refresh, and prove a route switch aborts the previous diff before the new
  sidebar mounts after runtime readiness.
- A seeded full-stack browser test proves EventSource delivery and workspace,
  runtime, and diff-request ordering across a real workspace switch.
- Before/after live traces record cold and warm HEAD/merge-target loads for the
  profiled workspace. The measured acceptance budgets are a default-HEAD cold
  preparation below 500 ms and warm `/files` and `/diff` server spans below
  5 ms each. Subprocess count must have zero slope as changed-file count grows
  from 1 to 128, and an unchanged validation must perform no preparation. Any
  regression beyond those budgets is investigated rather than hidden by cache
  warmth.
- The measured selected HEAD cache-hit server spans were 0.25 ms for `/files`
  and 0.35 ms for `/diff`. A concurrent cold merge-target preparation left the
  runtime endpoint at 0.675 ms server time, confirming Git work is off readiness.
