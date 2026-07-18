# Selected Workspace Diff Loading Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make selected local workspace diffs load from a prepared, bounded snapshot while reducing the cold path to aggregate Git work and recomputing only after resolved refs or changed contents move.

**Architecture:** Resolve each request into a logical key plus a Git/content fingerprint. The logical key carries workspace/path/base/scope/whitespace identity; the physical cache revision adds resolved base/head OIDs and the dirty-content digest, satisfying the resolved-base key invariant without forcing fresh validation on every warm read. A server-owned stale-while-revalidate cache stores the complete gitclone.DiffResult and a patch-free file projection, coalesces preparation, and validates only workspaces leased by the terminal SSE connection. Ordinary whitespace-only classification moves from one git diff -w subprocess per path to a Go implementation of Git xdiff record equivalence; explicit hide-whitespace patches remain aggregate Git -w output.

**Tech Stack:** Go 1.26, Git CLI through go.kenn.io/kit/git/cmd, jellydator/ttlcache/v3, golang.org/x/sync/singleflight, OpenTelemetry, Huma/OpenAPI, Svelte 5 runes, Vite+ Vitest.

## Global Constraints

- Proactively prepare and validate only a terminal-selected workspace: local
  selection uses scoped SSE and fleet selection holds a long-poll lease on the
  owning member.
- Preserve current /files, /diff, and /file-preview response shapes, base/scope semantics, rename/copy detection, --find-copies-harder, untracked files, generated attributes, path safety, and explicit Git -w hunk behavior.
- Use Git xdiff's ASCII C-locale whitespace set: space, tab, newline, vertical tab, form feed, and carriage return.
- Keep file previews as separate bounded reads; never store preview contents in the snapshot cache.
- Use a 15-second selected validation interval, a 10-minute inactivity TTL, and a 128 MiB approximate-byte ceiling. These are internal constants, not new configuration.
- Treat 15 seconds as the validation max-age for every entry. Unselected entries validate on demand after that age; selected entries also validate proactively.
- Replace a cached value only when fingerprints taken before and after preparation match. A failure never replaces the last-known-good snapshot.
- Return an opaque cache-generation/revision token from both projections and let `/diff` require the `/files` token so replacements between requests cannot mix revisions.
- Do not add go-git. Use the hardened Git wrapper for Git behavior,
  `jellydator/ttlcache/v3` for TTL/LRU/cost lifecycle, and the existing
  x/sync/singleflight dependency for cancellable error-returning preparation.
- Apply kenn-test-scope-discipline:test-scope-discipline before tests. Test middleman-owned logic, not Git/Go/Svelte library behavior.
- Apply svelte-code-writer and svelte-core-bestpractices before Svelte edits. Run vp exec svelte-mcp svelte-autofixer on every changed Svelte file.
- Apply the mandatory commit-push-pr:commit skill before every commit. Never amend and never bypass hooks.

---

## File Structure

- Create internal/workspace/diff_whitespace.go and diff_whitespace_test.go for Git-compatible record comparison and parsed-diff classification.
- Create internal/workspace/diff_snapshot.go and diff_snapshot_test.go for resolved inputs, ref/content fingerprinting, stable full preparation, and direct preview reads.
- Modify internal/workspace/diff.go so public workspace diff entry points share aggregate preparation and never classify with one subprocess per path.
- Create internal/server/workspace_diff_cache.go and workspace_diff_cache_test.go for bounded SWR storage, single-flight work, selection leases, and validators.
- Modify internal/server/server.go, huma_routes.go, and fleet_worktree_links.go to own the cache, route APIs through it, lease selection through SSE, and consume stats-change hints.
- Modify internal/server/server_test.go and api_test.go for wire/API behavior.
- Regenerate Huma OpenAPI and generated clients with make api-generate.
- Modify packages/ui/src/stores/diff.svelte.ts and its frontend test for stale-visible atomic replacement.
- Modify WorkspaceTerminalView.svelte, WorkspaceRightSidebar.svelte, WorkspaceDiffPanel.svelte, and focused tests for scoped selection and diff-only refresh.
- Modify context/workspace-apis.md with the final invariants.

---

### Task 1: Port Git xdiff whitespace equivalence and remove the per-file subprocess loop

**Files:**
- Create: internal/workspace/diff_whitespace.go
- Create: internal/workspace/diff_whitespace_test.go
- Modify: internal/workspace/diff.go:95-168,240-352,889-943
- Test: internal/workspace/diff_test.go

**Interfaces:**
- Consumes: parsed gitclone.DiffFile, Hunk, and Line values from one ordinary aggregate patch.
- Produces: classifyWhitespaceOnly(files []gitclone.DiffFile) int and gitWhitespaceRecordEqual(left, right string) bool.

- [ ] **Step 1: Write failing record and file classification tests**

Create table-driven tests for indentation, tabs, CR, vertical-tab/form-feed, blank-line insertion, missing final newline, repeated lines, mixed edits, multiple hunks, and status/binary guards.

~~~go
func TestGitWhitespaceRecordEqual(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name        string
		left, right string
		want        bool
	}{
		{name: "indentation", left: "\treturn value", right: "  return value", want: true},
		{name: "vertical and form feed", left: "a\vb\fc", right: "abc", want: true},
		{name: "substantive", left: "return old", right: "return new", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, gitWhitespaceRecordEqual(tt.left, tt.right))
		})
	}
}
~~~

For file classification, construct modified parsed hunks and assert added, deleted, renamed, copied, and binary files remain non-whitespace-only.

- [ ] **Step 2: Prove the new tests fail**

Run:

~~~bash
go test ./internal/workspace -run 'TestGitWhitespaceRecordEqual|TestClassifyWhitespaceOnly' -shuffle=on
~~~

Expected: FAIL because both functions are undefined.

- [ ] **Step 3: Implement exact byte-level record comparison**

~~~go
func isGitWhitespace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	default:
		return false
	}
}

func gitWhitespaceRecordEqual(left, right string) bool {
	i, j := 0, 0
	for {
		for i < len(left) && isGitWhitespace(left[i]) { i++ }
		for j < len(right) && isGitWhitespace(right[j]) { j++ }
		if i == len(left) || j == len(right) {
			for i < len(left) && isGitWhitespace(left[i]) { i++ }
			for j < len(right) && isGitWhitespace(right[j]) { j++ }
			return i == len(left) && j == len(right)
		}
		if left[i] != right[j] { return false }
		i++
		j++
	}
}
~~~

hunkWhitespaceOnly rebuilds old/new records: context enters both sides, delete only old, add only new. It requires equal record counts and pairwise equality. Ignore NoNewline because -w compares record contents. classifyWhitespaceOnly considers only modified non-binary files, requires every changed hunk to pass, mutates only IsWhitespaceOnly, and returns the count.

- [ ] **Step 4: Add Git-oracle parity cases**

For each old/new byte fixture, create a temp repo, commit old, write new, parse the ordinary WorktreeDiff, and compare the Go flag with whether git diff --quiet -w -- path exits zero. Include CRLF, repeated lines, blank-line insertion, missing-final-newline, and mixed whitespace/substantive edits.

If ordinary hunk reconstruction fails an oracle case, compare complete old/new record sequences obtained with one git cat-file --batch invocation plus Go worktree reads. Never add per-file Git calls.

- [ ] **Step 5: Route aggregate preparation through the classifier**

In worktreeDiffFromRefsPath, run raw, numstat, and patch once, parse the result, call classifyWhitespaceOnly in ordinary mode, and use its count. Explicit hide mode keeps aggregate Git -w raw/numstat/patch output and uses ordinary classification only for the count.

Make WorktreeDiffFiles variants project metadata from the same full preparation by copying entries, setting Patch to empty, and setting Hunks to []gitclone.Hunk{}. Remove the production call path through worktreeWhitespaceOnlyFiles.

Wrap the cold preparation phases in child spans named workspace.diff.git.raw, workspace.diff.git.numstat, workspace.diff.git.patch, workspace.diff.whitespace, workspace.diff.untracked, workspace.diff.generated_attributes, and workspace.diff.assemble. Spans carry counts/bytes only, never file paths or contents.

- [ ] **Step 6: Run workspace regressions**

~~~bash
go test ./internal/workspace -run 'TestGitWhitespace|TestClassifyWhitespaceOnly|TestWorktreeDiff' -shuffle=on
~~~

Expected: PASS, including existing rename/copy, generated, untracked, preview, and hide-whitespace coverage selected by the pattern.

- [ ] **Step 7: Commit**

~~~bash
git add internal/workspace/diff.go internal/workspace/diff_test.go internal/workspace/diff_whitespace.go internal/workspace/diff_whitespace_test.go
git commit -m "perf: eliminate per-file workspace whitespace diffs"
~~~

The body explains that Git remains the aggregate patch authority while Go applies xdiff-compatible record equivalence, eliminating subprocess growth with file count.

---

### Task 2: Resolve, fingerprint, and stably prepare one complete workspace snapshot

**Files:**
- Create: internal/workspace/diff_snapshot.go
- Create: internal/workspace/diff_snapshot_test.go
- Modify: internal/workspace/diff.go

**Interfaces:**

~~~go
type DiffSnapshotSpec struct {
	WorktreePath      string
	Base              WorktreeDiffBase
	MergeTargetBranch string
	FromSHA           string
	ToSHA             string
	HideWhitespace    bool
}

type ResolvedDiffSnapshotSpec struct {
	DiffSnapshotSpec
	BaseRef          string
	HeadRef          string
	BaseOID          string
	HeadOID          string
	IncludeUntracked bool
}

type DiffFingerprint string

func ResolveDiffSnapshotSpec(context.Context, DiffSnapshotSpec) (ResolvedDiffSnapshotSpec, bool, error)
func FingerprintDiffSnapshot(context.Context, ResolvedDiffSnapshotSpec) (DiffFingerprint, error)
func PrepareDiffSnapshot(context.Context, ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error)
func ReadDiffSnapshotFile(context.Context, ResolvedDiffSnapshotSpec, gitclone.DiffFile, string, int64) (*gitclone.FileContent, error)
~~~

- [ ] **Step 1: Write failing resolution/fingerprint tests**

Test HEAD, pushed, merge-target, commit, and range resolution; stable unchanged fingerprints; same-size/same-line-count dirty edits; untracked content edits; and commit/range fingerprints that ignore unrelated dirty files.

- [ ] **Step 2: Prove the tests fail**

~~~bash
go test ./internal/workspace -run 'TestResolveDiffSnapshotSpec|TestFingerprintDiffSnapshot' -shuffle=on
~~~

Expected: FAIL because the snapshot types/functions do not exist.

- [ ] **Step 3: Implement verified ref resolution**

Normalize WorktreePath with filepath.Abs and filepath.Clean. Commit/range uses FromSHA/ToSHA with IncludeUntracked=false. Other scopes reuse worktreeDiffBaseRef or worktreeMergeTargetBaseRef, use the worktree as the head, and IncludeUntracked=true. Resolve every non-empty ref through hardened git rev-parse --verify ref^{commit}.

- [ ] **Step 4: Implement the content fingerprint**

Hash a versioned stream containing normalized path, freshly resolved OIDs, mode flags, aggregate git diff --raw -z --no-renames metadata, repository-local `.git/info/attributes`, and git ls-files --others --exclude-standard -z when untracked files participate. For each current worktree path in those sets, append lstat mode plus symlink target or a content digest. Cache content digests by strong stat identity so unchanged large files are not reread on every validation. Encode missing/deleted state explicitly. Commit/range snapshots use immutable resolved OIDs.

Do not use addition/deletion totals as identity.

- [ ] **Step 5: Implement preparation and direct preview**

PrepareDiffSnapshot calls the aggregate ref preparer from Task 1. ReadDiffSnapshotFile accepts already-selected DiffFile metadata and reads old/new content directly through existing blob/worktree safety and byte limits; it must not run a diff to rediscover membership.

- [ ] **Step 6: Test movement during preparation**

Use a narrow unexported test hook restored with t.Cleanup to edit a file or move a ref between before/after fingerprints. Re-resolve the logical spec after preparation and assert the resolved OIDs or fingerprints differ so the server can reject publication. Document boundary comparison as best-effort movement detection, not a transactional filesystem snapshot. Do not add retry machinery to the workspace package.

- [ ] **Step 7: Run and commit**

~~~bash
go test ./internal/workspace -shuffle=on
git add internal/workspace/diff.go internal/workspace/diff_snapshot.go internal/workspace/diff_snapshot_test.go
git commit -m "feat: prepare stable workspace diff snapshots"
~~~

The body explains that immutable refs plus dirty-content hashing separate cheap validation from recomputation and let previews reuse resolved membership.

---

### Task 3: Add the bounded stale-while-revalidate cache and selection leases

**Files:**
- Create: internal/server/workspace_diff_cache.go
- Create: internal/server/workspace_diff_cache_test.go

**Interfaces:**

~~~go
type workspaceDiffLogicalKey struct {
	WorkspaceID string
	Spec        workspace.DiffSnapshotSpec
}

type workspaceDiffSnapshot struct {
	Resolved    workspace.ResolvedDiffSnapshotSpec
	Fingerprint workspace.DiffFingerprint
	Revision    uint64
	Diff        *gitclone.DiffResult
	Files       []gitclone.DiffFile
	SizeBytes   int64
}

type workspaceDiffCacheState string

func (c *workspaceDiffCache) Get(context.Context, workspaceDiffLogicalKey) (*workspaceDiffSnapshot, workspaceDiffCacheState, error)
func (c *workspaceDiffCache) Select(string, func(context.Context) (workspaceDiffLogicalKey, error)) func()
func (c *workspaceDiffCache) MarkActive(workspaceDiffLogicalKey)
func (c *workspaceDiffCache) ValidateSelected()
~~~

- [ ] **Step 1: Write failing cache tests with fake time/dependencies**

Inject now, resolve, fingerprint, prepare, and onChanged. Cover first miss/fresh hit; concurrent coalescing; immediate stale return; equal validation without prepare/event; changed stable replacement; movement during prepare; last-known-good after failure; byte/TTL eviction with inactive-first LRU; first-selection HEAD prewarm; tab refcounting; final-disconnect validator stop.

Use channels only for coalescing and immediate-stale contracts. Use fake-clock advancement for TTL.

- [ ] **Step 2: Prove the tests fail**

~~~bash
go test ./internal/server -run 'TestWorkspaceDiffCache' -shuffle=on
~~~

Expected: FAIL because workspaceDiffCache is undefined.

- [ ] **Step 3: Implement storage and projections**

Use `jellydator/ttlcache/v3` for logical-entry storage and TTL expiration. Keep
protected-entry cost pressure, physical fingerprint/revision state,
selected/pair-retention protection, stable publication, and
`singleflight.Group` in the workspace coordinator. Approximate bytes by summing
patch/path/status/hunk/line strings; never JSON-marshal on the request path.

Build Files by copying each DiffFile, clearing Patch, and replacing Hunks with an empty non-nil slice.

- [ ] **Step 4: Implement hit/stale/miss and stable replacement**

Fresh returns synchronously. Expired last-known-good returns a clone marked stale and schedules validation. A cold miss waits. Shared work runs under a cache-owned 30-second context and callers wait via singleflight.DoChan, so a five-second validator or canceled request does not cancel other waiters.

Stable publication re-resolves the logical spec on both sides of preparation:

~~~go
beforeResolved := resolve(logicalSpec)
before := fingerprint(beforeResolved)
result := prepare(beforeResolved) // exclusively from resolved OIDs
afterResolved := resolve(logicalSpec)
after := fingerprint(afterResolved)
if beforeResolved.OIDs != afterResolved.OIDs || before != after {
	return errWorkspaceDiffMovedDuringPreparation
}
publish(result, after)
~~~

Preparation errors or movement never replace last-known-good or emit an event.

- [ ] **Step 5: Implement selected-only validation and eviction**

Select refcounts workspace IDs. First lease resolves/prewarms default HEAD and starts one 15-second loop. MarkActive retains every recently requested base/scope/whitespace key for a selected workspace, so concurrent tabs cannot overwrite each other's validation target. Final release cancels the loop but retains entries.

ValidateSelected debounces prompt validation for selected active keys and queues
it through one background worker; that worker remains attached until
cache-owned preparation completes. A failed initial prewarm retries every five
seconds for the life of the selection. Preserve active scopes across watch
reconnects, prune them after 10 idle minutes, then evict inactive entries by LRU
toward 128 MiB while skipping active and one-minute pair-retained snapshots.

- [ ] **Step 6: Add trace data**

Set request-span attributes workspace.diff.cache_result, workspace.diff.snapshot_bytes, workspace.diff.revision, and workspace.id. cache_result is exactly hit, stale, miss, or coalesced; use singleflight's shared result plus the cache's pre-call state to distinguish miss from coalesced. Add child spans workspace.diff.resolve, workspace.diff.fingerprint, and workspace.diff.prepare around the granular preparation spans from Task 1; record errors and concurrent movement. Never attach paths or content.

- [ ] **Step 7: Run normal/race tests and commit**

~~~bash
go test ./internal/server -run 'TestWorkspaceDiffCache' -shuffle=on
go test -race ./internal/server -run 'TestWorkspaceDiffCache' -shuffle=on
git add internal/server/workspace_diff_cache.go internal/server/workspace_diff_cache_test.go
git commit -m "feat: cache selected workspace diff snapshots"
~~~

The body records selected-only cost, stale-while-revalidate behavior, and stable publication.

---

### Task 4: Wire the cache into APIs, worktree signals, SSE, and shutdown

**Files:**
- Modify: internal/server/server.go:130-255,675-840,1458-1605
- Modify: internal/server/huma_routes.go:557-585,657-665,850-880,5808-6250
- Modify: internal/server/fleet_worktree_links.go:283-291
- Modify: internal/server/server_test.go
- Modify: internal/server/api_test.go:25713-26630
- Modify: generated files from make api-generate

**Interfaces:**
- Produces: GET /api/v1/events?workspace_id={local-id} selection lease and workspace_diff_changed payload with workspace_id and revision.

- [ ] **Step 1: Add failing wire/API tests**

Through httptest.Server, open a scoped stream, wait for one lease/default HEAD preparation, open a second and assert one validator/two refs, close both, and assert final release. An unscoped stream creates no lease. Parse a replacement frame:

~~~json
{"workspace_id":"ws-1","revision":2}
~~~

Using existing workspace API fixtures plus an injected counting preparer, request /files then /diff and assert one preparation with identical file/count identity. Concurrent requests coalesce. A path request after whole-snapshot preparation filters without preparation. Retain existing preview/path-safety tests. Add one real HTTP + SQLite + temporary-Git-worktree e2e scenario that makes a same-size edit, validates it, observes `workspace_diff_changed`, and reads the replacement.

- [ ] **Step 2: Prove the tests fail**

~~~bash
go test ./internal/server -run 'TestWorkspaceDiffSelection|TestWorkspaceDiffEndpointsShareSnapshot|TestWorkspaceDiffEndpointScopesPatchByPathE2E|TestWorkspaceFilePreviewEndpointReturnsRequestedDiffSideContentE2E' -shuffle=on
~~~

Expected: FAIL in new lease/cache assertions.

- [ ] **Step 3: Own cache lifecycle in Server**

Add workspaceDiffCache *workspaceDiffCache. Construct it after bgCtx/workspace manager dependencies exist. Use s.bgCtx as root so existing shutdown cancellation and background drain stop validators/in-flight work; do not create an untracked shutdown path.

- [ ] **Step 4: Route all workspace reads through the snapshot**

Convert workspaceDiffRequest to workspaceDiffLogicalKey after current scope validation. /files returns snapshot.Files; /diff returns snapshot.Diff.Files; both use the same whitespace count/stale state and opaque generation/revision token. `/diff?revision=` rejects a token mismatch so the client restarts both reads instead of publishing mixed revisions. Path-scoped /diff filters an exact Path or OldPath match from a copy. /file-preview selects membership from the snapshot then calls workspace.ReadDiffSnapshotFile.

Preserve workspaceDiffBaseUnavailable and current cold-failure problem envelopes.

- [ ] **Step 5: Register SSE selection and stats hints**

Add a Huma query input:

~~~go
type streamEventsInput struct {
	WorkspaceID string
}
~~~

Tag WorkspaceID as query workspace_id with documentation. Capture it in streamEvents, acquire before serveSSE, and defer release. Invalid/not-ready IDs log background prewarm failure without breaking the general event stream.

notifyWorktreeStatsChanged calls workspaceDiffCache.ValidateSelected before broadcasting the existing stats event. It never scans every workspace.

- [ ] **Step 6: Broadcast only stable replacements**

On a changed stable replacement broadcast workspace_diff_changed with WorkspaceID and Revision. This replacement callback emits nothing for equal validation, initial cold population, failure, or concurrent movement; Task 7 adds a separate selected-prewarm readiness event.

- [ ] **Step 7: Regenerate and verify**

~~~bash
make api-generate
go test ./internal/server -run 'TestHumaContractMetadata|TestHumaConvenienceRoutesUseDocumentOperation|TestRouteMetadataWalker|TestWorkspaceDiffSelection|TestWorkspaceDiffEndpoint' -shuffle=on
~~~

Expected: PASS with intentional generated changes.

- [ ] **Step 8: Commit**

Stage server code, tests, and generated artifacts, then:

~~~bash
git commit -m "perf: serve workspace diff views from shared snapshots"
~~~

The body explains that /files prepares the whole snapshot, /diff is a projection, and SSE lifetime owns local selection.

---

### Task 5: Preserve stale UI data and refresh only the matching workspace diff

**Files:**
- Modify: packages/ui/src/stores/diff.svelte.ts:16-25,640-865
- Modify: frontend/src/lib/stores/diff.svelte.test.ts
- Modify: frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte:180-225,2460-2600,3055-3080
- Modify: frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts
- Modify: packages/ui/src/components/workspace/WorkspaceRightSidebar.svelte:41-78,297-310
- Modify: packages/ui/src/components/workspace/WorkspaceDiffPanel.svelte:8-55

**Interfaces:**
- Consumes workspace_diff_changed data {workspace_id, revision}.
- Produces LoadWorkspaceDiffOptions.preserveVisible, diffSnapshotRevision props, and atomic background replacement.

- [ ] **Step 1: Add failing stale-visible store tests**

Load snapshot A. Start a preserving refresh with deferred files/diff. Assert A remains visible. Resolve files B only and assert A remains coherent. Resolve diff B with the pinned revision and assert both projections switch together. Simulate a revision mismatch and assert the pair retries rather than mixing. Reject refresh and assert A remains while getDiffError records failure.

~~~ts
const refresh = store.loadWorkspaceDiff("ws-1", "head", false, {
  preserveVisible: true,
});
~~~

- [ ] **Step 2: Prove current clearing behavior fails**

~~~bash
./node_modules/.bin/vp test frontend/src/lib/stores/diff.svelte.test.ts
~~~

Expected: FAIL in the new test because startDiffLoad clears current state.

- [ ] **Step 3: Implement atomic preserving refresh**

Add preserveVisible to LoadWorkspaceDiffOptions. Preserving loads retain reactive state, keep the new files response in a local non-reactive variable, and apply files+diff only after both succeed. Failure retains old values and sets the error. Initial/base/scope/whitespace loads keep current progressive files-then-diff behavior.

Do not add shared reactive pending payloads; one invocation and existing generation guards own them.

- [ ] **Step 4: Add failing terminal selection/event tests**

Capture EventSource URLs/listeners. Assert local ws-1 opens events?workspace_id=ws-1; fleet member/ws-1 remains unscoped; another workspace event does nothing; a different matching opaque version triggers only diff refresh; replaying the same version does not duplicate work. SSE event IDs, not revision values, own ordering.

- [ ] **Step 5: Thread a diff-only revision**

Add diffSnapshotRevision state in WorkspaceTerminalView. Defensively parse the event, require the matching local ID and a changed opaque generation/revision token, then assign it. On `reconnect.stale`, trigger the same preserving diff refresh. Pass it separately from sidebarRefreshToken so PR/issue/review panels do not redraw.

Add the prop through WorkspaceRightSidebar to WorkspaceDiffPanel. The panel key includes general refresh and diff revision. preserveVisible is true only when workspace/base identity is unchanged and the diff revision advanced. refreshCommits remains tied to manual/general refresh, not background diff replacement.

- [ ] **Step 6: Scope only local SSE**

Within the existing route effect:

~~~ts
const evtURL = new URL(basePath + "/api/v1/events", window.location.origin);
if (!hostKey) evtURL.searchParams.set("workspace_id", id);
const source = new EventSource(evtURL.pathname + evtURL.search);
~~~

Existing teardown closes the source, ending the server lease on navigation/destruction.

- [ ] **Step 7: Run Svelte analysis and focused tests**

~~~bash
./node_modules/.bin/vp exec svelte-mcp svelte-autofixer ./frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte --svelte-version 5
./node_modules/.bin/vp exec svelte-mcp svelte-autofixer ./packages/ui/src/components/workspace/WorkspaceRightSidebar.svelte --svelte-version 5
./node_modules/.bin/vp exec svelte-mcp svelte-autofixer ./packages/ui/src/components/workspace/WorkspaceDiffPanel.svelte --svelte-version 5
./node_modules/.bin/vp test frontend/src/lib/stores/diff.svelte.test.ts frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts
~~~

Expected: no new actionable autofixer issue from edits; tests PASS.

- [ ] **Step 8: Commit**

~~~bash
git add packages/ui/src/stores/diff.svelte.ts packages/ui/src/components/workspace/WorkspaceRightSidebar.svelte packages/ui/src/components/workspace/WorkspaceDiffPanel.svelte frontend/src/lib/stores/diff.svelte.test.ts frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts
git commit -m "perf: refresh selected workspace diffs without blanking"
~~~

The body explains that scoped SSE owns local selection and the last coherent snapshot stays visible during replacement.

---

### Task 6: Document invariants and verify cold/warm traces

**Files:**
- Modify: context/workspace-apis.md:170-195
- Modify the design spec only if final names materially differ.

- [ ] **Step 1: Record durable invariants**

Add a Diff snapshot lifecycle subsection stating: /files and /diff project one complete snapshot; ordinary whitespace classification is Go xdiff-equivalent while explicit hide uses aggregate Git -w; only events?workspace_id creates proactive local selection; stats change is a selected validation hint; equal fingerprints do not recompute/event; stable replacement emits workspace_diff_changed; last-known-good survives failures. Anchor claims to final functions.

- [ ] **Step 2: Run complete affected backend suites**

~~~bash
go test ./internal/workspace ./internal/server -shuffle=on
~~~

Expected: PASS.

- [ ] **Step 3: Run complete frontend unit suite**

~~~bash
./node_modules/.bin/vp test
~~~

Expected: PASS. Reproduce any claimed unrelated scheduling failure on the base commit rather than weakening/skipping tests.

- [ ] **Step 4: Run static/build checks**

~~~bash
make lint
make frontend
git diff --check
~~~

Expected: PASS with only intentional generated artifacts.

- [ ] **Step 5: Measure the profiled live workspace**

For workspace 539fb58f99084088 and a checked-in synthetic 128-file benchmark fixture, record cold/warm HEAD and merge-target /files then /diff spans. Verify cold preparation uses constant aggregate Git commands; following /diff is a hit with no Git work; unchanged selected validation fingerprints only; a real edit produces one replacement/event; warm latency is transfer/serialization rather than Git. Record cold latency, warm lookup latency, validation duration, subprocess count, and bytes hashed so future runs are comparable.

If any named cold phase remains multi-second, investigate/fix it before completion instead of hiding it with cache warmth.

- [ ] **Step 6: Run context-sync**

~~~bash
scripts/context-sync --check
scripts/hooks/context-sync-stop.sh mark "updated context/workspace-apis.md for selected workspace diff snapshot lifecycle"
~~~

Address drift before marking.

- [ ] **Step 7: Commit**

~~~bash
git add context/workspace-apis.md
git commit -m "docs: record workspace diff cache invariants"
~~~

The body explains why selected-only validation and last-known-good publication must survive refactors.

---

### Task 7: Give workspace runtime priority over sidebar diff work

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`
- Modify: `frontend/tests/e2e-full/00-workspace-tab-persistence.spec.ts`
- Modify: `packages/ui/src/components/workspace/WorkspaceDiffPanel.svelte`
- Modify: `packages/ui/src/stores/diff.svelte.ts`
- Modify: `frontend/src/lib/stores/diff.svelte.test.ts`
- Modify: `internal/server/workspace_diff_cache.go`
- Modify: `internal/server/workspace_diff_cache_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/api_test.go`

**Interfaces:**
- Consumes: current route identity, `runtimeLive`, and the diff store's current workspace identity.
- Produces: `cancelWorkspaceDiff(workspaceID, workspaceHostKey?, loadToken?)`, an identity- and invocation-scoped abort that cannot cancel a newer same-workspace load.
- Produces: a monotonic workspace-load generation checked after every await, including commit refresh.
- Produces: selected-prewarm `workspace_diff_ready` SSE payload `{workspace_id, revision, version}`.

- [ ] **Step 1: Add failing transition and cancellation tests**

In `WorkspaceTerminalView.test.ts`, keep workspace A unresolved after selecting B and assert A's PR/diff content is absent, a `Loading workspace details...` placeholder is visible, and the B diff request does not start until both B workspace and runtime responses apply. Also reject B's runtime request and assert matching B workspace details become usable instead of leaving the sidebar spinner forever. Deliver `workspace_diff_ready` before runtime resolves and assert it is retained without starting a browser diff request. In `diff.svelte.test.ts`, start A, start B, then call `cancelWorkspaceDiff("A")`; assert B is not aborted or cleared. Start two same-workspace loads and prove cleanup from the first token cannot abort the second. Also cancel A while its commit refresh is unresolved, resolve that request, and assert the stale invocation cannot start files/diff requests afterward.

- [ ] **Step 2: Run focused tests to verify they fail**

~~~bash
./node_modules/.bin/vp test frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts frontend/src/lib/stores/diff.svelte.test.ts
~~~

Expected: FAIL because stale sidebar content remains mounted and no identity-scoped cancellation API exists.

- [ ] **Step 3: Implement shell-first sidebar lifecycle**

Render the right-sidebar frame whenever it is open, but render `WorkspaceRightSidebar` only when the loaded workspace identity matches the route and runtime loading has either produced matching state or settled with an error. Render a neutral spinner with `Loading workspace details...` during the transition. Do not clear the previous workspace/runtime objects because the workflow pane intentionally remains stable.

Add a monotonic workspace-load generation and an invocation token to the diff store and capture them at the start of every load. `cancelWorkspaceDiff(workspaceID, workspaceHostKey?, loadToken?)` returns without mutation unless the supplied identity and, when present, token equal the current load; on a match it advances the generation, aborts both controllers, and clears only workspace diff loading state. Check identity and generation after every await and in every rejection path, including `loadCommits`, and before starting each request. Track the active invocation outside the reactive load key and cancel it explicitly on identity change, deactivation, or component destroy. Do not return cancellation directly from the load effect: that effect updates `loadedKey`, so its own rerun would abort the request it just started.

- [ ] **Step 4: Emit readiness after selected server prewarm**

Add a selected-prewarm callback distinct from ordinary cache replacement. Register the event-hub subscriber synchronously before acquiring the selection lease and starting prewarm; only then enter the SSE serve loop. In `workspaceDiffCache.Select`, when the first lease's `Get` publishes or coalesces a cold default-HEAD snapshot, invoke the callback with its revision/version. `Server` broadcasts `workspace_diff_ready`; cached hits, failures, unselected cold requests, and normal validation do not emit it. Keep `workspace_diff_changed` for replacement of an existing fingerprint. The client treats versions as opaque equality tokens; SSE event IDs, not version strings, own event ordering and replay.

Add cache and wire-level SSE tests proving one ready event follows successful first-selection cold preparation, no event follows failure/hit, and the payload is scoped to the selected workspace. The wire test must let prewarm complete immediately and prove the first selecting client receives readiness, guarding subscriber-before-lease ordering.

- [ ] **Step 5: Run Svelte analysis and focused tests**

~~~bash
./node_modules/.bin/vp exec svelte-mcp svelte-autofixer frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte --svelte-version 5
./node_modules/.bin/vp exec svelte-mcp svelte-autofixer packages/ui/src/components/workspace/WorkspaceDiffPanel.svelte --svelte-version 5
(cd frontend && ../node_modules/.bin/vp test src/lib/components/terminal/WorkspaceTerminalView.test.ts src/lib/stores/diff.svelte.test.ts)
~~~

Expected: no new actionable autofixer issue; tests PASS.

- [ ] **Step 6: Run backend tests**

~~~bash
go test ./internal/server -run 'TestWorkspaceDiffCache|TestWorkspaceDiffSelectionLease' -shuffle=on
~~~

Expected: PASS.

- [ ] **Step 7: Run the complete frontend suite and capture the transition**

~~~bash
(cd frontend && ../node_modules/.bin/vp test)
make frontend-check
make frontend
(cd frontend && node ./scripts/run-e2e-to-file.ts 00-workspace-tab-persistence.spec.ts --project=chromium)
~~~

Add a seeded full-stack scenario that opens workspace A, switches to B while B's workspace/runtime responses are gated, and observes the real EventSource. Assert A's sidebar disappears immediately, early B readiness does not start a browser diff, and B's diff request starts only after matching workspace/runtime data apply. Expected: PASS. Capture the same neutral transition from this scenario without exposing a live workspace.

- [ ] **Step 8: Commit and update the existing PR**

~~~bash
git add frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts frontend/tests/e2e-full/00-workspace-tab-persistence.spec.ts packages/ui/src/components/workspace/WorkspaceDiffPanel.svelte packages/ui/src/stores/diff.svelte.ts frontend/src/lib/stores/diff.svelte.test.ts internal/server/workspace_diff_cache.go internal/server/workspace_diff_cache_test.go internal/server/server.go internal/server/api_test.go
git commit -m "fix: prioritize workspace runtime during switches"
git push
~~~

The body records that old diff response parsing must never compete with readiness of the newly selected workspace shell.

---

## Final Acceptance Checklist

- [ ] A 128-file merge-target cold load performs no per-file Git subprocess loop.
- [ ] /files followed by /diff prepares once; the second request is a projection.
- [ ] Warm selected-workspace requests perform no Git diff work.
- [ ] Unchanged validation performs no recomputation and emits no event.
- [ ] Same-total content edits are detected; concurrent edits cannot publish a torn snapshot.
- [ ] Only terminal selections receive proactive work; fleet selection is leased on the owning member and inactive workspaces stay request-driven.
- [ ] Cache prunes active keys after 10 minutes without access and evicts inactive entries toward a 128 MiB approximate-byte target while preserving last-known-good after failures.
- [ ] Matching workspace_diff_changed refreshes only the diff panel without blanking.
- [ ] Switching workspace identity removes the old sidebar immediately, aborts its diff load, and starts the new diff only after runtime readiness.
- [ ] Selected default-HEAD preparation begins immediately and emits one readiness event without putting browser diff parsing on the shell critical path.
- [ ] Focused, full affected, race, Svelte analysis, generated-contract, lint, build, and context-sync checks pass.
