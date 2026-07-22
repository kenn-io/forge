# Kata Selected-History Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Load complete selected-task history from Kata's project event API and make repeated local or federated selections reuse bounded five-second in-memory enrichment caches.

**Architecture:** Global issue/event reads remain workspace bootstrap and invalidation authority. Selected enrichment uses generated `ShowIssueByUID`, fully paginated `PollProjectEvents`, and reachable-graph reads; a daemon-scoped non-touching TTL cache stores immutable daemon responses and is cleared with the existing daemon invalidation epoch.

**Tech Stack:** Go 1.25, generated `go.kenn.io/kata/pkg/client/generated` client, `github.com/jellydator/ttlcache/v3`, `golang.org/x/sync/singleflight`, Huma server tests, Playwright Kata E2E.

## Global Constraints

- Global Kata issue/event reads establish workspace authority and invalidation; they are not the sole API used for selected detail or history.
- Selected history uses generated `PollProjectEvents`, fully paginates to an empty page, filters by issue UID, and returns every matching event.
- There is no scan-page limit, scanned-event limit, returned-history limit, fallback global scan, or compatibility path.
- Every enrichment cache has a five-second non-touching TTL and bounded entry capacity; eviction affects reuse only and never truncates results.
- Detail cache identity is daemon epoch plus issue UID; project-event identity is daemon epoch plus project ID; graph identity is daemon epoch plus source UID and graph options.
- SSE invalidation, mutation acknowledgement, daemon target rotation, and restart generation changes make every prior enrichment entry for that daemon unusable.
- Middleman persists no Kata task, event, snapshot, cursor, detail, history, or graph state.

---

### Task 1: Replace global bounded history scans with complete project history

**Files:**
- Modify: `internal/server/kata_client.go`
- Modify: `internal/server/kata_snapshot_enrichment.go`
- Modify: `internal/server/kata_snapshot_loader_test.go`
- Modify: `internal/server/kata_snapshot_enrichment_test.go`
- Modify: `internal/server/kata_snapshot_routes_test.go`

**Interfaces:**
- Consumes: generated `PollProjectEventsWithResponse(context.Context, *generated.PollProjectEventsRequestOptions, ...runtime.RequestEditorFn)`.
- Produces: `loadHistory(ctx context.Context, projectID int64, selectedUID string) ([]generated.EventEnvelope, error)` returning the complete matching history.

- [ ] **Step 1: Write a failing complete-history regression test**

Replace the cap-oriented history tests with one test that returns two non-empty project pages followed by an empty page. Put more than 100 matching events on the second page and assert every match is returned. Assert every request carries project ID `7` and that the cursors are `0`, the first page's `next_after_id`, and the second page's `next_after_id`.

```go
func TestKataSnapshotEnricherLoadsCompleteSelectedHistoryFromProjectEvents(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	selectedUID := "issue-member"
	otherUID := "issue-other"
	cursors := []int64{}
	client := &fakeKataSnapshotAPIClient{
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			return testKataShowIssueResponse(selectedUID), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			require.Equal(t, int64(7), options.PathParams.ProjectID)
			cursors = append(cursors, *options.Query.AfterID)
			switch len(cursors) {
			case 1:
				return testKataPollProjectEventsResponse(2,
					testKataEvent(1, &otherUID, time.Unix(1, 0)),
					testKataEvent(2, &selectedUID, time.Unix(2, 0))), nil
			case 2:
				events := testKataEventPage(3, 125, &selectedUID)
				return testKataPollProjectEventsResponse(127, events...), nil
			default:
				return testKataPollProjectEventsResponse(127), nil
			}
		},
	}

	result, err := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client}).Enrich(
		t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: selectedUID},
	)

	require.NoError(t, err)
	assert.Len(result.SelectedHistory, 126)
	assert.Equal([]int64{0, 2, 127}, cursors)
	assert.NotContains(result.Errors, kataSnapshotEnrichmentStageHistory)
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./internal/server -run TestKataSnapshotEnricherLoadsCompleteSelectedHistoryFromProjectEvents -shuffle=on
```

Expected: compile failure because `pollProjectEvents`, `PollProjectEventsWithResponse`, and the project response helper do not exist.

- [ ] **Step 3: Add the generated project-event method to the owned client seam**

Add this method to `kataAPIClient` and the shared fake:

```go
PollProjectEventsWithResponse(
	ctx context.Context,
	options *katagenerated.PollProjectEventsRequestOptions,
	reqEditors ...runtime.RequestEditorFn,
) (*katagenerated.PollProjectEventsResp, error)
```

The fake field and method are:

```go
pollProjectEvents func(context.Context, *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error)

func (f *fakeKataSnapshotAPIClient) PollProjectEventsWithResponse(
	ctx context.Context,
	options *katagenerated.PollProjectEventsRequestOptions,
	_ ...runtime.RequestEditorFn,
) (*katagenerated.PollProjectEventsResp, error) {
	if f.pollProjectEvents == nil {
		return &katagenerated.PollProjectEventsResp{
			StatusCode: http.StatusOK,
			JSON200:    &katagenerated.PollEventsBody{NextAfterID: 0},
		}, nil
	}
	return f.pollProjectEvents(ctx, options)
}
```

Keep `PollEventsWithResponse` because the event hub still owns the global invalidation stream.

- [ ] **Step 4: Implement complete project pagination**

Delete `kataSnapshotHistoryScanPageLimit`, `kataSnapshotHistoryScanEventLimit`, `kataSnapshotHistoryResultLimit`, and `errKataSnapshotHistoryScanLimit`. Change the call site to `e.loadHistory(ctx, selected.ProjectID, selected.UID)` and implement:

```go
func (e *kataSnapshotEnricher) loadHistory(
	ctx context.Context,
	projectID int64,
	selectedUID string,
) ([]katagenerated.EventEnvelope, error) {
	history := []katagenerated.EventEnvelope{}
	afterID := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limit := kataSnapshotHistoryPageLimit
		response, err := e.client.PollProjectEventsWithResponse(ctx, &katagenerated.PollProjectEventsRequestOptions{
			PathParams: &katagenerated.PollProjectEventsPath{ProjectID: projectID},
			Query:      &katagenerated.PollProjectEventsQuery{AfterID: &afterID, Limit: &limit},
		})
		if err != nil {
			return nil, err
		}
		body, err := validateKataHistoryPage(response, afterID)
		if err != nil {
			return nil, err
		}
		if len(body.Events) == 0 {
			return history, nil
		}
		for _, event := range body.Events {
			if event.IssueUID != nil && *event.IssueUID == selectedUID {
				event.CreatedAt = event.CreatedAt.UTC()
				history = append(history, event)
			}
		}
		afterID = body.NextAfterID
	}
}
```

Keep the existing response/body validation inline or extract `validateKataHistoryPage`; do not weaken cursor, reset, monotonic-ID, cancellation, or generated validation checks.

- [ ] **Step 5: Remove obsolete cap/error tests and update route expectations**

Delete only tests whose contract is the removed scan/result cap. Retain and adapt malformed response, cursor mismatch, cancellation, detail/history independence, HTTP serialization, and graph authorization tests to use `PollProjectEvents` and assert project ID `7`. The HTTP scan-limit case becomes a successful multi-page project-history case.

- [ ] **Step 6: Run Task 1 verification**

Run:

```bash
go test ./internal/server -run 'TestKataSnapshotEnricher|TestKataTaskSnapshot' -shuffle=on
go run ./cmd/testify-helper-check ./internal/server
git diff --check
```

Expected: all commands exit 0; no `selected task history scan limit exceeded` reference remains.

- [ ] **Step 7: Commit Task 1**

Use `$context-sync --commit`, then `$commit` with subject:

```text
fix: load complete Kata project history
```

---

### Task 2: Cache daemon enrichment reads by daemon epoch

**Files:**
- Create: `internal/server/kata_snapshot_enrichment_cache.go`
- Create: `internal/server/kata_snapshot_enrichment_cache_test.go`
- Modify: `internal/server/kata_snapshot_enrichment.go`
- Modify: `internal/server/kata_snapshot_frontend.go`
- Modify: `internal/server/kata_snapshot_coordinator.go`
- Modify: `internal/server/kata_snapshot_coordinator_test.go`
- Modify: `internal/server/kata_snapshot_frontend_test.go`

**Interfaces:**
- Consumes: `kataCoordinatedAuthority.DaemonID` and `.InvalidationEpoch`.
- Produces: `kataSnapshotEnrichmentCache` with typed `issueDetail`, `projectEvents`, `graph`, `invalidateDaemon`, `run`, and `close` operations.

- [ ] **Step 1: Write failing cache reuse and invalidation tests**

Create tests using a short TTL and atomic load counters:

```go
func TestKataSnapshotEnrichmentCacheSharesProjectEventsAcrossIssues(t *testing.T) {
	t.Parallel()
	cache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8)
	t.Cleanup(cache.close)
	var loads atomic.Int64
	key := kataProjectEventsCacheKey{DaemonID: "local", DaemonEpoch: 3, ProjectID: 7}
	load := func(context.Context) ([]katagenerated.EventEnvelope, error) {
		loads.Add(1)
		return []katagenerated.EventEnvelope{testKataEvent(1, nil, time.Unix(1, 0))}, nil
	}

	first, err := cache.projectEvents(t.Context(), key, load)
	require.NoError(t, err)
	second, err := cache.projectEvents(t.Context(), key, load)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, int64(1), loads.Load())

	cache.invalidateDaemon("local")
	_, err = cache.projectEvents(t.Context(), key, load)
	require.NoError(t, err)
	assert.Equal(t, int64(2), loads.Load())
}
```

Add parallel tests proving non-touching expiry, bounded capacity eviction without result truncation, detail/graph key separation, and singleflight coalescing of concurrent identical loads.

- [ ] **Step 2: Run cache tests and verify RED**

Run:

```bash
go test ./internal/server -run TestKataSnapshotEnrichmentCache -shuffle=on
```

Expected: compile failure because the enrichment cache types do not exist.

- [ ] **Step 3: Implement typed non-touching TTL caches**

Create `kata_snapshot_enrichment_cache.go` with three typed caches:

```go
type kataIssueDetailCacheKey struct {
	DaemonID    string
	DaemonEpoch uint64
	IssueUID    string
}

type kataProjectEventsCacheKey struct {
	DaemonID    string
	DaemonEpoch uint64
	ProjectID   int64
}

type kataGraphCacheKey struct {
	DaemonID    string
	DaemonEpoch uint64
	SourceUID   string
	Depth       string
	HideDone    bool
}

type kataSnapshotEnrichmentCache struct {
	details *ttlcache.Cache[kataIssueDetailCacheKey, kataCachedIssueDetail]
	events  *ttlcache.Cache[kataProjectEventsCacheKey, []katagenerated.EventEnvelope]
	graphs  *ttlcache.Cache[kataGraphCacheKey, *katagenerated.ReachableGraphResponseBody]
	detailGroup singleflight.Group
	eventGroup  singleflight.Group
	graphGroup  singleflight.Group
	// mutex-protected per-daemon key indexes for targeted invalidation
}
```

Construct every cache with `ttlcache.WithTTL`, `ttlcache.WithCapacity`, and `ttlcache.WithDisableTouchOnHit`. Clone event slices on cache set/get. Treat generated detail and graph values as immutable after validation. `invalidateDaemon` deletes only that daemon's indexed keys. `run` periodically calls `DeleteExpired`; `close` unregisters eviction callbacks.

- [ ] **Step 4: Route enricher daemon reads through the cache**

Extend `kataSnapshotEnricherDeps`:

```go
cache *kataSnapshotEnrichmentCache
```

Inside `Enrich`, construct keys from `authority.DaemonID`, `authority.InvalidationEpoch`, the selected issue, project ID, and graph options. Cache the complete project event slice before filtering by selected UID, so two selected issues in one project share one remote pagination sequence. Cache the generated issue detail plus ETag and the raw validated graph response.

The cache-miss functions continue to use the request context and existing generated-client validation. Cache errors are not stored.

- [ ] **Step 5: Own cache lifecycle and invalidation in the coordinator**

Add `enrichmentCache *kataSnapshotEnrichmentCache` to `kataSnapshotCoordinator`. Default it in `newKataSnapshotCoordinator`. Start its expiry loop with the authority cache, close it during coordinator shutdown, and call `enrichmentCache.invalidateDaemon(daemonID)` whenever `invalidateDaemon` advances the authority epoch.

Target rotation and restart generation already change the authority epoch; include the epoch in every enrichment key so an entry loaded under the old target cannot be accepted even before asynchronous eviction completes.

Pass `s.kataSnapshots.enrichmentCache` into `newKataSnapshotEnricher` from `kataSnapshotFrontend`.

- [ ] **Step 6: Add frontend/coordinator integration tests**

Add tests proving:

1. two identical `Snapshot` calls inside five seconds make one detail call and one project-history pagination sequence;
2. two different selected issues in project `7` share the project-history sequence but load separate details;
3. `invalidateDaemon("local")` forces fresh detail, project history, and graph reads;
4. an old-epoch cache hit cannot be delivered after target rotation.

Use owned fake counters and observable responses; do not test `ttlcache` library internals.

- [ ] **Step 7: Run Task 2 verification**

Run:

```bash
go test ./internal/server -run 'TestKataSnapshotEnrichmentCache|TestKataSnapshotFrontend|TestKataSnapshotCoordinator|TestKataTaskSnapshot' -shuffle=on
go run ./cmd/testify-helper-check ./internal/server
make frontend-check-no-deps
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 8: Commit Task 2**

Use `$context-sync --commit`, then `$commit` with subject:

```text
perf: cache Kata selected enrichment
```

---

### Task 3: Verify the real local and federated selection paths

**Files:**
- Modify if required by the reproduction: `internal/server/e2etest/kata_snapshot_test.go`
- Modify if required by visible regression coverage: `frontend/tests/e2e/kata.spec.ts`

**Interfaces:**
- Consumes: the completed snapshot endpoint and running `make dev-ephemeral` stack.
- Produces: runtime evidence that recent issue history loads without warnings and repeated remote selections use cached enrichment.

- [ ] **Step 1: Add the narrowest missing full-stack regression**

If the Go HTTP tests do not already exercise a selected issue whose matching events occur after more than 4,000 unrelated global events, add one real server test that seeds that shape but exposes a short project event stream. Assert the response contains selected history and no history enrichment error. Do not duplicate this in Playwright unless the warning rendering/removal itself lacks frontend coverage.

- [ ] **Step 2: Run repository verification**

Run:

```bash
go test ./internal/server ./internal/server/e2etest -shuffle=on
go run ./cmd/testify-helper-check ./internal/server ./internal/server/e2etest
make frontend-check-no-deps
scripts/context-sync --check
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 3: Verify through the live ephemeral stack**

Use the existing `tmp/dev-ephemeral/dev-ephemeral.json` URLs. Select `w5dh` and confirm the response has no `enrichment.errors.history`. Select it twice and select another issue in the same project; verify backend timings/logged daemon calls show cache reuse within five seconds. Repeat against the `kenn` daemon and record before/after selection latency.

- [ ] **Step 4: Exercise the UI with Computer Use**

Use `$computer-use:computer-use` through `node_repl` to open the ephemeral frontend, switch between local and federated daemons, select multiple issues, inspect history, and confirm the warning is absent. If the native service still rejects the authenticated sender, request approval before restarting the exact `SkyComputerUseService` process; do not substitute another automation tool for this required check.

- [ ] **Step 5: Run `$roborev-fix` only against open automatic findings**

Inspect `roborev fix --open --list`; do not create a review. Fix and close only existing failing jobs following the skill workflow.

- [ ] **Step 6: Final commit and push**

Use `$context-sync --commit`, `$commit`, then push the existing PR branch after the full affected suite passes. Do not amend, force-push, resolve comments, or add compatibility scaffolding.
