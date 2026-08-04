# Detached HEAD Divergence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop routine upstream probes from reporting detached managed worktrees as unexpected Git failures.

**Approved spec/design:** `docs/superpowers/specs/2026-08-03-detached-head-divergence-design.md`

**Architecture:** Keep upstream availability classification inside the shared workspace Git probe boundary. Extend the existing stderr classifier with Git's exact detached-`HEAD` response so both divergence and unpushed-commit callers return their established unavailable result without adding subprocesses or sampler-specific behavior.

**Tech Stack:** Go, Git CLI, Testify

## Global Constraints

- Detached `HEAD` returns zero data, `ok=false`, and no error from both upstream probes.
- Other `git rev-list` failures remain errors.
- Do not add a routine Git subprocess.

---

### Task 1: Classify detached upstream probes as unavailable

**Files:**
- Modify: `internal/workspace/divergence_test.go`
- Modify: `internal/workspace/divergence.go`

**Interfaces:**
- Consumes: `WorktreeDivergence(context.Context, string) (Divergence, bool, error)` and `WorktreeUnpushedSHAs(context.Context, string) (map[string]struct{}, bool, error)`.
- Produces: The existing `isNoUpstreamMessage(string) bool` classifier recognizes Git's detached-`HEAD` response for both callers.

- [ ] **Step 1: Add real-Git detached-HEAD regression tests**

Add these focused tests beside the existing no-upstream cases in `internal/workspace/divergence_test.go`:

```go
func TestWorktreeDivergenceDetachedHead(t *testing.T) {
	work := setupDivergenceWorktree(t)
	runWorkspaceTestGit(t, work, "checkout", "--detach")

	div, ok, err := WorktreeDivergence(t.Context(), work)
	require.NoError(t, err)
	assert.False(t, ok, "expected ok=false for detached HEAD")
	assert.Equal(t, Divergence{}, div)
}

func TestWorktreeUnpushedSHAsDetachedHead(t *testing.T) {
	work := setupDivergenceWorktree(t)
	runWorkspaceTestGit(t, work, "checkout", "--detach")

	unpushed, ok, err := WorktreeUnpushedSHAs(t.Context(), work)
	require.NoError(t, err)
	assert.False(t, ok, "expected ok=false for detached HEAD")
	assert.Nil(t, unpushed)
}
```

These tests catch removal or omission of detached-`HEAD` classification by exercising the actual Git failure through both public probe functions.

- [ ] **Step 2: Run the tests and verify the regression is red**

Run:

```bash
go test -shuffle=on ./internal/workspace -run 'TestWorktree(Divergence|UnpushedSHAs)DetachedHead'
```

Expected: FAIL because each probe returns `git rev-list: exit status 128: fatal: HEAD does not point to a branch`.

- [ ] **Step 3: Add the minimal shared classification**

Extend `isNoUpstreamMessage` in `internal/workspace/divergence.go` without changing its callers:

```go
return strings.Contains(s, "no upstream configured") ||
	strings.Contains(s, "unknown revision") ||
	strings.Contains(s, "no such ref") ||
	strings.Contains(s, "ambiguous argument") ||
	strings.Contains(s, "head does not point to a branch")
```

- [ ] **Step 4: Verify focused and package behavior is green**

Run:

```bash
go test -shuffle=on ./internal/workspace -run 'TestWorktree(Divergence|UnpushedSHAs)'
go test -shuffle=on ./internal/workspace
```

Expected: PASS with no probe warnings or errors.

- [ ] **Step 5: Run repository short verification**

Run:

```bash
make test-short
```

Expected: PASS.

- [ ] **Step 6: Commit the implementation**

Run the repository-local `context-sync --commit` workflow, then the mandatory commit skill. Stage only:

```bash
git add internal/workspace/divergence.go internal/workspace/divergence_test.go docs/superpowers/plans/2026-08-03-detached-head-divergence.md
git commit -m "fix: stop detached worktrees from raising probe warnings"
```

The commit body should explain that detached worktrees are intentional last-resort states and therefore have no meaningful upstream divergence.
