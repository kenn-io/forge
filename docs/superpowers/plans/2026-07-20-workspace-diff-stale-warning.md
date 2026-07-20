# Workspace Diff Stale Warning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make workspace diff responses set `stale: true` only when the cached snapshot head differs from the currently resolved Git HEAD.

**Architecture:** Keep age-based stale-while-revalidate bookkeeping inside `workspaceDiffCache.Get`, but decouple it from the user-visible `gitclone.DiffResult.Stale` flag. On cache hits, resolve the current snapshot spec after releasing the cache mutex and compare its `HeadOID` with the cached snapshot's `Resolved.HeadOID`; resolution failure or unavailability is not a confirmed mismatch.

**Tech Stack:** Go, `testify`, the existing workspace diff snapshot cache and server API tests.

## Global Constraints

- Cache age must continue to schedule asynchronous validation.
- Only a confirmed cached/current `HeadOID` mismatch may set the workspace response's `stale` flag.
- Failed or unavailable current-head resolution must serve the last-known-good snapshot without the warning.
- Pull-request diff staleness is unchanged.
- Run direct Go tests with `-shuffle=on`, without `-count=1` or `-v`.

---

### Task 1: Derive workspace warning state from Git HEAD

**Files:**
- Modify: `internal/server/workspace_diff_cache.go:171-197`
- Test: `internal/server/workspace_diff_cache_test.go`
- Verify: `internal/server/api_test.go`

**Interfaces:**
- Consumes: `workspaceDiffCacheDeps.resolve(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error)` and `workspaceDiffSnapshot.Resolved.HeadOID`.
- Produces: cache-hit `workspaceDiffSnapshot.Diff.Stale`, true only for a confirmed `HeadOID` mismatch; `workspaceDiffCacheState` continues to report age-based cache state for tracing and validation.

- [ ] **Step 1: Write the failing cache regression test**

Add a table-driven `TestWorkspaceDiffCacheWarningRequiresHeadMismatch` covering matching, changed, unavailable, and failed current-head resolution. Create a fresh cache per case, populate it, age its entry, prevent the unrelated async validator from racing the assertion with `retryAfter`, then change the resolver outcome before the second `Get`:

```go
func TestWorkspaceDiffCacheWarningRequiresHeadMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		currentHead string
		resolveOK  bool
		resolveErr error
		wantStale  bool
	}{
		{name: "matching head", currentHead: "head", resolveOK: true},
		{name: "changed head", currentHead: "new-head", resolveOK: true, wantStale: true},
		{name: "head unavailable"},
		{name: "head resolution failed", resolveErr: errors.New("resolve head")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			now := time.Unix(100, 0)
			resolved := workspaceDiffTestResolved()
			resolveOK := true
			var resolveErr error
			cache := newWorkspaceDiffCache(t.Context(), workspaceDiffCacheDeps{
				now: func() time.Time { return now },
				resolve: func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error) {
					return resolved, resolveOK, resolveErr
				},
				fingerprint: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error) {
					return "v1", nil
				},
				prepare: func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error) {
					return workspaceDiffTestResult("one.txt"), nil
				},
			})

			_, _, err := cache.Get(t.Context(), workspaceDiffTestKey())
			require.NoError(err)
			now = now.Add(workspaceDiffCacheFreshFor + time.Second)
			cache.mu.Lock()
			cache.peekEntryLocked(workspaceDiffTestKey()).retryAfter = now.Add(time.Hour)
			cache.mu.Unlock()
			resolved.HeadOID = tt.currentHead
			resolveOK = tt.resolveOK
			resolveErr = tt.resolveErr

			got, state, err := cache.Get(t.Context(), workspaceDiffTestKey())
			require.NoError(err)
			require.NotNil(got)
			assert.Equal(workspaceDiffCacheStale, state)
			assert.Equal(tt.wantStale, got.Diff.Stale)
		})
	}
}
```

- [ ] **Step 2: Run the regression test and verify RED**

Run:

```bash
go test ./internal/server -run TestWorkspaceDiffCacheWarningRequiresHeadMismatch -shuffle=on
```

Expected: FAIL because the current implementation sets `Diff.Stale` from cache age, making the matching, unavailable, and failed-resolution cases stale.

- [ ] **Step 3: Implement the minimal cache-hit comparison**

In `workspaceDiffCache.Get`, keep the cached snapshot and age state while holding the mutex, then release the mutex before calling `resolve`. Derive the cloned response's warning flag only from a successful, available head comparison:

```go
		fresh := now.Sub(updated.validatedAt) <= workspaceDiffCacheFreshFor
		retryAllowed := !now.Before(updated.retryAfter)
		cached := updated.snapshot
		c.mu.Unlock()
		resolved, ok, resolveErr := c.deps.resolve(ctx, key.Spec)
		headMoved := resolveErr == nil && ok && cached.Resolved.HeadOID != resolved.HeadOID
		snapshot := cloneWorkspaceDiffSnapshot(cached, headMoved)
		state := workspaceDiffCacheHit
		if !fresh {
			state = workspaceDiffCacheStale
			if retryAllowed {
				c.validateAsync(key)
			}
		}
```

Do not change `refresh`, cache versions, event publication, PR SHA comparisons, or the frontend banner.

- [ ] **Step 4: Run the regression test and verify GREEN**

Run:

```bash
go test ./internal/server -run TestWorkspaceDiffCacheWarningRequiresHeadMismatch -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Verify age-based validation still runs**

Run the existing selected-validation tests:

```bash
go test ./internal/server -run 'TestWorkspaceDiffCache(SelectedValidationMeetsMaxAge|ReconnectRetainsActiveScopes)' -shuffle=on
```

Expected: PASS, proving the age threshold still schedules validation independently of the warning flag.

- [ ] **Step 6: Verify the affected cache and HTTP boundary**

Run:

```bash
go test ./internal/server -run 'TestWorkspaceDiff(Cache|EndpointsReportHeadAndPushed)' -shuffle=on
```

Then run:

```bash
go test ./internal/server -shuffle=on
```

Expected: both commands PASS.

- [ ] **Step 7: Sync context and commit**

Run the repository-local `context-sync` skill in `--commit` mode, review the final diff, then use the mandatory commit skill to create a conventional commit such as:

```text
fix: warn only when workspace diff head moved
```

The commit body should explain that cache age continues to drive background validation but no longer masquerades as confirmed Git revision drift.
