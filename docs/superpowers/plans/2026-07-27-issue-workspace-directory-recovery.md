# Issue Workspace Directory Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely re-register an issue's deterministic Middleman worktree after its workspace database row was lost.

**Architecture:** Add an explicit issue-workspace request flag that preflights only the deterministic worktree path before inserting a replacement row. The manager reuses its existing provenance checks, the server exposes stable failure reasons, and the Svelte dialog keeps branch reuse, directory recovery, and new-branch creation separate.

**Tech Stack:** Go, Huma/OpenAPI, SQLite, Svelte 5, TypeScript, Testing Library, Vite+, Playwright.

## Global Constraints

- Recovery accepts no path and considers only the deterministic Middleman issue-worktree directory.
- The directory must be an owned linked worktree for the same provider, host, owner, repository, and requested branch.
- Recovery must not move a branch, reset `HEAD`, clean files, remove directories, or modify dirty/untracked contents.
- `Use Existing Branch` remains separate and unchanged for branches Git can check out into a new worktree.
- Invalid directories fail before database insertion with a stable machine-readable problem.
- Default-host and explicit-host provider routes expose identical request fields.
- Regenerate API artifacts with `make api-generate`.
- Direct Go tests use `-shuffle=on` and never `-count=1` or `-v`.
- Frontend commands use Bun/Vite+; never npm.
- Before each commit, run `context-sync --commit` and the mandatory commit skill. Never amend or bypass hooks.

---

## File Map

- `internal/workspace/manager.go`: recovery option, validation, typed reasons.
- `internal/workspace/manager_test.go`: preservation and rejection tests.
- `internal/server/workspaceapi/routes_handlers.go`: request mapping and problem translation.
- `internal/server/provider_route_wrappers.go`: explicit-host request parity.
- `internal/server/httpapi/problems.go` and `problems_test.go`: closed problem-code taxonomy.
- `internal/server/workspacetest/fixtures_test.go` and new `issue_workspace_directory_recovery_test.go`: generated-client HTTP coverage.
- `frontend/openapi/openapi.yaml`, `internal/apiclient/generated/client.gen.go`, and `packages/ui/src/api/generated/*`: generated contract.
- `context/error-handling.md` and `context/workspace-apis.md`: durable error/recovery invariants.
- `packages/ui/src/components/detail/IssueDetail.svelte` and `IssueDetail.test.ts`: dialog action and local errors.
- `frontend/tests/e2e-full/detail-action-buttons.spec.ts`: visible inline-workspace recovery.

---

### Task 1: Validate and register the deterministic directory

**Files:**
- Modify: `internal/workspace/manager.go`
- Test: `internal/workspace/manager_test.go`

**Interfaces:**
- Consumes: `existingWorkspaceWorktreeProvenance`, `worktreeCommonGitDir`, `gitDirMatchesWorkspaceRepo`, `gitDirOwnsLinkedWorktree`, and `worktreeCurrentBranch`.
- Produces: `CreateIssueOptions.ReuseExistingDirectory bool` and `WorkspaceDirectoryRecoveryError`.

- [ ] **Step 1: Write the failing successful-recovery test**

Add `TestCreateIssueRecoversExpectedExistingDirectory`. Use `openTestDB`, `setupHTTPWorktreeBaseForWorkspaceGitTest`, `seedRepo`, and `seedIssue`. Create the linked worktree at the exact path:

~~~go
worktreeRoot := t.TempDir()
const branch = "middleman/issue-7"
expectedPath := filepath.Join(
	worktreeRoot, "github", platformHost, "acme", "widget", "issue-7",
)
runWorkspaceTestGit(
	t, localRepo, "worktree", "add", expectedPath, "-b", branch, "HEAD",
)
require.NoError(os.WriteFile(
	filepath.Join(expectedPath, "base.txt"), []byte("dirty\n"), 0o644,
))
require.NoError(os.WriteFile(
	filepath.Join(expectedPath, "untracked.txt"), []byte("keep\n"), 0o644,
))

mgr := NewManager(d, worktreeRoot)
mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))
ws, err := mgr.CreateIssue(
	t.Context(), platformHost, "acme", "widget", 7,
	CreateIssueOptions{
		Provider: "github",
		GitHeadRef: branch,
		ReuseExistingDirectory: true,
	},
)
require.NoError(err)
require.NotNil(ws)
assert.Equal(expectedPath, ws.WorktreePath)
assert.Equal(branch, ws.WorkspaceBranch)
assert.FileExists(filepath.Join(expectedPath, "untracked.txt"))
assert.Contains(
	strings.TrimSpace(string(runWorkspaceTestGit(t, expectedPath, "status", "--short"))),
	"base.txt",
)
~~~

- [ ] **Step 2: Verify RED**

Run:

~~~bash
go test ./internal/workspace -run TestCreateIssueRecoversExpectedExistingDirectory -shuffle=on
~~~

Expected: compilation fails because `ReuseExistingDirectory` is absent.

- [ ] **Step 3: Write failing table-driven rejection coverage**

Add `TestCreateIssueRecoveryRejectsInvalidExpectedDirectory` with four literal cases:

~~~go
tests := []struct {
	name       string
	prepare    func(t *testing.T, localRepo, expectedPath, branch string)
	wantReason WorkspaceDirectoryRecoveryReason
}{
	{
		name: "missing",
		prepare: func(*testing.T, string, string, string) {},
		wantReason: WorkspaceDirectoryMissing,
	},
	{
		name: "ordinary directory",
		prepare: func(t *testing.T, _, expectedPath, _ string) {
			require.NoError(t, os.MkdirAll(expectedPath, 0o755))
		},
		wantReason: WorkspaceDirectoryNotLinkedWorktree,
	},
	{
		name: "wrong branch",
		prepare: func(t *testing.T, localRepo, expectedPath, _ string) {
			runWorkspaceTestGit(
				t, localRepo, "worktree", "add", expectedPath,
				"-b", "other/branch", "HEAD",
			)
		},
		wantReason: WorkspaceDirectoryBranchMismatch,
	},
	{
		name: "wrong repository",
		prepare: func(t *testing.T, _, expectedPath, branch string) {
			otherRepo := filepath.Join(t.TempDir(), "other")
			runWorkspaceTestGit(
				t, filepath.Dir(otherRepo), "init", "--initial-branch=main", otherRepo,
			)
			runWorkspaceTestGit(t, otherRepo, "config", "user.email", "test@test.com")
			runWorkspaceTestGit(t, otherRepo, "config", "user.name", "Test")
			require.NoError(t, os.WriteFile(
				filepath.Join(otherRepo, "base.txt"), []byte("base\n"), 0o644,
			))
			runWorkspaceTestGit(t, otherRepo, "add", ".")
			runWorkspaceTestGit(t, otherRepo, "commit", "-m", "base")
			runWorkspaceTestGit(
				t, otherRepo, "worktree", "add", expectedPath, "-b", branch, "HEAD",
			)
		},
		wantReason: WorkspaceDirectoryRepositoryMismatch,
	},
}
~~~

Inline each case's isolated DB/repository/root setup. After `CreateIssue`, assert `require.ErrorAs` to `*WorkspaceDirectoryRecoveryError`, the literal reason, and a nil result from `GetWorkspaceByIssueForProvider`.

- [ ] **Step 4: Verify rejection tests are RED**

Run:

~~~bash
go test ./internal/workspace -run TestCreateIssueRecoveryRejectsInvalidExpectedDirectory -shuffle=on
~~~

Expected: compilation fails because the recovery error types are absent.

- [ ] **Step 5: Add typed reasons**

Add next to `WorkspaceBranchConflictError`:

~~~go
type WorkspaceDirectoryRecoveryReason string

const (
	WorkspaceDirectoryMissing WorkspaceDirectoryRecoveryReason = "missing"
	WorkspaceDirectoryNotLinkedWorktree WorkspaceDirectoryRecoveryReason = "not_linked_worktree"
	WorkspaceDirectoryRepositoryMismatch WorkspaceDirectoryRecoveryReason = "repository_mismatch"
	WorkspaceDirectoryBranchMismatch WorkspaceDirectoryRecoveryReason = "branch_mismatch"
)

type WorkspaceDirectoryRecoveryError struct {
	Reason         WorkspaceDirectoryRecoveryReason
	ExpectedBranch string
	ActualBranch   string
}

func (e *WorkspaceDirectoryRecoveryError) Error() string {
	switch e.Reason {
	case WorkspaceDirectoryMissing:
		return "the expected Middleman worktree directory does not exist"
	case WorkspaceDirectoryNotLinkedWorktree:
		return "the expected Middleman directory is not a linked Git worktree"
	case WorkspaceDirectoryRepositoryMismatch:
		return "the expected Middleman worktree does not belong to this repository"
	case WorkspaceDirectoryBranchMismatch:
		return fmt.Sprintf(
			"the expected Middleman worktree checks out %q, not %q",
			e.ActualBranch, e.ExpectedBranch,
		)
	default:
		return "the expected Middleman worktree cannot be reused"
	}
}
~~~

Add `ReuseExistingDirectory bool` to `CreateIssueOptions`.

- [ ] **Step 6: Add non-mutating preflight**

Implement:

~~~go
func (m *Manager) validateExistingWorkspaceDirectory(
	ctx context.Context, ws *Workspace,
) error {
	info, err := os.Stat(ws.WorktreePath)
	if errors.Is(err, os.ErrNotExist) {
		return &WorkspaceDirectoryRecoveryError{
			Reason: WorkspaceDirectoryMissing,
		}
	}
	if err != nil {
		return fmt.Errorf("stat expected workspace directory: %w", err)
	}
	if !info.IsDir() {
		return &WorkspaceDirectoryRecoveryError{
			Reason: WorkspaceDirectoryNotLinkedWorktree,
		}
	}
	commonDir, err := worktreeCommonGitDir(ctx, ws.WorktreePath)
	if err != nil {
		if isGitWorktreeAbsent(err) {
			return &WorkspaceDirectoryRecoveryError{
				Reason: WorkspaceDirectoryNotLinkedWorktree,
			}
		}
		return fmt.Errorf("inspect expected workspace directory: %w", err)
	}
	if !gitDirMatchesWorkspaceRepo(ctx, commonDir, ws) {
		return &WorkspaceDirectoryRecoveryError{
			Reason: WorkspaceDirectoryRepositoryMismatch,
		}
	}
	owned, err := gitDirOwnsLinkedWorktree(ctx, commonDir, ws.WorktreePath)
	if err != nil {
		return fmt.Errorf("inspect expected linked worktree: %w", err)
	}
	if !owned {
		return &WorkspaceDirectoryRecoveryError{
			Reason: WorkspaceDirectoryNotLinkedWorktree,
		}
	}
	_, reusable, err := m.existingWorkspaceWorktreeProvenance(ctx, commonDir, ws)
	if err != nil {
		return fmt.Errorf("validate expected workspace provenance: %w", err)
	}
	if !reusable {
		return &WorkspaceDirectoryRecoveryError{
			Reason: WorkspaceDirectoryRepositoryMismatch,
		}
	}
	actualBranch, err := worktreeCurrentBranch(ctx, ws.WorktreePath)
	if err != nil {
		return fmt.Errorf("inspect expected workspace branch: %w", err)
	}
	if actualBranch != ws.GitHeadRef {
		return &WorkspaceDirectoryRecoveryError{
			Reason: WorkspaceDirectoryBranchMismatch,
			ExpectedBranch: ws.GitHeadRef,
			ActualBranch: actualBranch,
		}
	}
	return nil
}
~~~

In `CreateIssue`, retain ordinary branch-conflict logic only when recovery is false. Construct the normal issue workspace and deterministic path, call the validator when recovery is true, then insert. Never fall back to creating a new path from this action.

- [ ] **Step 7: Verify GREEN and regression safety**

Run:

~~~bash
go test ./internal/workspace -run 'TestCreateIssue(RecoversExpectedExistingDirectory|RecoveryRejectsInvalidExpectedDirectory|ReuseLocalBaseBranchCheckedOutReturnsConflict)' -shuffle=on
~~~

Expected: PASS.

- [ ] **Step 8: Commit Task 1**

Run context sync and the commit skill. Stage only the manager and its tests. Commit subject:

~~~text
feat: recover orphaned issue worktree directories
~~~

The body must explain that database restoration can lose the row while the deterministic linked worktree survives, and that recovery preserves contents after repository/branch validation.

---

### Task 2: Add the stable server/API recovery contract

**Files:**
- Modify: `internal/server/workspaceapi/routes_handlers.go`
- Modify: `internal/server/provider_route_wrappers.go`
- Modify: `internal/server/httpapi/problems.go`
- Modify: `internal/server/httpapi/problems_test.go`
- Modify: `internal/server/workspacetest/fixtures_test.go`
- Create: `internal/server/workspacetest/issue_workspace_directory_recovery_test.go`
- Regenerate: `frontend/openapi/openapi.yaml`
- Regenerate: `internal/apiclient/generated/client.gen.go`
- Regenerate: `packages/ui/src/api/generated/schema.ts`
- Regenerate if changed: `packages/ui/src/api/generated/client.ts`
- Modify: `context/error-handling.md`
- Modify: `context/workspace-apis.md`

**Interfaces:**
- Consumes: Task 1's recovery option and typed error.
- Produces: `reuse_existing_directory` and `workspaceDirectoryNotReusable` with `reason`, `expectedBranch`, and `actualBranch` details.

- [ ] **Step 1: Write the failing generated-client success test**

Add `worktreeDir string` to `workspaceServerFixture` and populate it from the existing fixture variable. In the new test file, create the exact managed-clone worktree and call the generated client:

~~~go
expectedPath := filepath.Join(
	fixture.worktreeDir, "github", "github.com", "acme", "widget", "issue-7",
)
branch := "middleman/issue-7"
runGit(t, fixture.bare, "worktree", "add", expectedPath, "-b", branch, "main")
require.NoError(os.WriteFile(
	filepath.Join(expectedPath, "untracked.txt"), []byte("keep\n"), 0o644,
))

reuseDirectory := true
resp, err := fixture.client.HTTP.CreateIssueWorkspaceWithResponse(
	t.Context(), "gh", "acme", "widget", 7,
	generated.CreateIssueWorkspaceInputBody{
		GitHeadRef: &branch,
		ReuseExistingDirectory: &reuseDirectory,
	},
)
require.NoError(err)
require.Equal(http.StatusAccepted, resp.StatusCode(), string(resp.Body))
require.NotNil(resp.JSON202)
ready := waitForWorkspaceReady(t, t.Context(), fixture.client, resp.JSON202.Id)
assert.Equal(expectedPath, ready.WorktreePath)
assert.Equal(branch, workspaceGitOutput(
	t, expectedPath, "branch", "--show-current",
))
assert.FileExists(filepath.Join(expectedPath, "untracked.txt"))
~~~

- [ ] **Step 2: Verify server success is RED**

Run:

~~~bash
go test ./internal/server/workspacetest -run TestIssueWorkspaceRecoversExpectedDirectory -shuffle=on
~~~

Expected: compilation fails because the generated request field is absent.

- [ ] **Step 3: Write the failing missing-directory response test**

Without creating the path, make the same request and assert:

~~~go
require.Equal(http.StatusConflict, resp.StatusCode(), string(resp.Body))
problem := resp.ApplicationproblemJSONDefault
require.NotNil(problem)
assert.Equal(generated.WorkspaceDirectoryNotReusable, problem.Code)
require.NotNil(problem.Details)
assert.Equal("missing", (*problem.Details)["reason"])
workspace, err := fixture.database.GetWorkspaceByIssueForProvider(
	t.Context(), "github", "github.com", "acme", "widget", 7,
)
require.NoError(err)
assert.Nil(workspace)
~~~

- [ ] **Step 4: Verify the response test is RED**

Run:

~~~bash
go test ./internal/server/workspacetest -run TestIssueWorkspaceDirectoryRecoveryRejectsMissingPath -shuffle=on
~~~

Expected: compilation fails because the field/code are absent.

- [ ] **Step 5: Add request parity and manager mapping**

Add to both issue request body structs:

~~~go
ReuseExistingDirectory bool `json:"reuse_existing_directory,omitempty"`
~~~

Pass it through:

~~~go
ReuseExistingDirectory: input.Body.ReuseExistingDirectory,
~~~

The host wrapper's whole-`Body` assignment must continue to compile, proving identical shapes.

- [ ] **Step 6: Add and map the stable code**

Add alphabetically to the problem constant list, enum struct tag, and `allProblemCodes`:

~~~go
CodeWorkspaceDirectoryNotReusable ProblemCode = "workspaceDirectoryNotReusable"
~~~

Before generic manager-error handling in `createIssueWorkspace`:

~~~go
var recoveryErr *workspace.WorkspaceDirectoryRecoveryError
if errors.As(err, &recoveryErr) {
	details := map[string]any{
		"reason": string(recoveryErr.Reason),
	}
	if recoveryErr.ExpectedBranch != "" {
		details["expectedBranch"] = recoveryErr.ExpectedBranch
	}
	if recoveryErr.ActualBranch != "" {
		details["actualBranch"] = recoveryErr.ActualBranch
	}
	return nil, httpapi.Conflict(
		httpapi.CodeWorkspaceDirectoryNotReusable,
		recoveryErr.Error(),
		details,
	)
}
~~~

Unexpected I/O errors stay `internalError`.

- [ ] **Step 7: Regenerate the checked-in contract**

Run:

~~~bash
make api-generate
~~~

Confirm both issue request bodies contain `reuse_existing_directory` and Go/TS problem enums contain `workspaceDirectoryNotReusable`. Reject unrelated generated changes.

- [ ] **Step 8: Document the invariant**

Add to `context/error-handling.md`:

~~~markdown
| `workspaceDirectoryNotReusable` | 409 | The deterministic Middleman issue-worktree directory cannot be recovered. Include `details.reason` as `missing`, `not_linked_worktree`, `repository_mismatch`, or `branch_mismatch`; branch mismatches also include `expectedBranch` and `actualBranch`. |
~~~

Add a concise paragraph to `context/workspace-apis.md`: issue directory recovery accepts no path, only recreates a missing row for the deterministic issue directory, validates repository provenance and branch before insertion, preserves contents, and is revalidated during setup.

- [ ] **Step 9: Verify GREEN**

Run:

~~~bash
go test ./internal/server/httpapi ./internal/server/workspacetest -run 'Test(ProblemErrorEnumTagMatchesConstants|IssueWorkspace)' -shuffle=on
make api-generate
git diff --check
~~~

Expected: tests pass and regeneration yields no additional diff.

- [ ] **Step 10: Commit Task 2**

Run context sync and the commit skill. Stage only route, error, test, generated, and context files. Commit subject:

~~~text
feat: expose deterministic workspace recovery
~~~

The body must explain why clients need a distinct recovery branch and why validation precedes persistence.

---

### Task 3: Add the conflict-dialog recovery action

**Files:**
- Modify: `packages/ui/src/components/detail/IssueDetail.svelte`
- Test: `packages/ui/src/components/detail/IssueDetail.test.ts`
- Test: `frontend/tests/e2e-full/detail-action-buttons.spec.ts`

**Interfaces:**
- Consumes: `reuse_existing_directory?: boolean` and the stable problem detail.
- Produces: `CreateWorkspaceOptions.reuseExistingDirectory?: boolean`, `Use Existing Directory`, and dialog-local resubmission errors.

- [ ] **Step 1: Write the failing component success test**

Use a POST mock that returns the existing branch conflict first and `{ data: { id: "ws-recovered", status: "provisioning" } }` second. Render with `createTestController("split")`, click `Create Workspace`, then `Use Existing Directory`. Assert:

~~~ts
expect(apiClient.POST.mock.calls[1]?.[1]).toMatchObject({
  body: {
    git_head_ref: "middleman/issue-7",
    reuse_existing_directory: true,
  },
});
await vi.waitFor(() => {
  expect(controller.recordCreated).toHaveBeenCalledWith(identity, {
    id: "ws-recovered",
    status: "provisioning",
  });
});
~~~

- [ ] **Step 2: Verify component success is RED**

Run:

~~~bash
./node_modules/.bin/vp test run --project unit packages/ui/src/components/detail/IssueDetail.test.ts -t "recovers the expected existing workspace directory"
~~~

Expected: FAIL because the button is absent.

- [ ] **Step 3: Write the failing component rejection test**

Return the branch conflict first, then:

~~~ts
{
  error: {
    code: "workspaceDirectoryNotReusable",
    detail: "the expected Middleman worktree directory does not exist",
    details: { reason: "missing" },
  },
}
~~~

After clicking the directory action, assert the dialog remains and contains the literal detail.

- [ ] **Step 4: Verify component rejection is RED**

Run:

~~~bash
./node_modules/.bin/vp test run --project unit packages/ui/src/components/detail/IssueDetail.test.ts -t "keeps directory recovery errors in the conflict dialog"
~~~

Expected: FAIL.

- [ ] **Step 5: Add request and local-error behavior**

Extend the options:

~~~ts
type CreateWorkspaceOptions = {
  gitHeadRef?: string;
  reuseExistingBranch?: boolean;
  reuseExistingDirectory?: boolean;
  fromConflictDialog?: boolean;
};
~~~

Add to the body:

~~~ts
...(options.reuseExistingDirectory
  ? { reuse_existing_directory: true }
  : {}),
~~~

For a non-initial error from a conflict-dialog request:

~~~ts
const message = requestError.detail
  ?? requestError.title
  ?? "failed to create workspace";
if (options.fromConflictDialog && branchConflict) {
  branchConflict.error = message;
  return;
}
throw new Error(message);
~~~

For a repeated branch conflict after `Use Existing Branch`, retain the parsed branch/suggestion and set:

~~~text
This branch is already checked out in another worktree. Use the existing Middleman directory or create a new branch.
~~~

This fixes the original inert-button presentation without changing successful branch reuse.

- [ ] **Step 6: Render the separate option**

Between branch reuse and the new-branch field, reuse `.branch-conflict-option`:

~~~svelte
<div class="branch-conflict-option">
  <div>
    <div class="branch-conflict-heading">Use the existing directory</div>
    <div class="branch-conflict-copy">
      Re-register the worktree at this issue's expected Middleman directory.
    </div>
  </div>
  <Button
    class="btn btn--primary"
    onclick={() => void createWorkspace({
      gitHeadRef: conflict.existingBranch,
      reuseExistingDirectory: true,
      fromConflictDialog: true,
    })}
    disabled={workspaceCreating}
    tone="neutral"
    surface="outline"
    size="sm"
  >
    {workspaceCreating ? "Creating..." : "Use Existing Directory"}
  </Button>
</div>
~~~

Add no one-off styling.

- [ ] **Step 7: Run Svelte analysis and unit tests**

Run:

~~~bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer ./packages/ui/src/components/detail/IssueDetail.svelte --svelte-version 5
./node_modules/.bin/vp test run --project unit packages/ui/src/components/detail/IssueDetail.test.ts
~~~

Expected: tests pass. Existing `{@html}` and established effect suggestions are not new findings.

- [ ] **Step 8: Add visible Playwright coverage**

Alongside the branch-reuse test, route the first POST to the branch conflict and the second to recovery success. Assert exact payloads:

~~~ts
[
  {},
  {
    git_head_ref: "middleman/issue-10",
    reuse_existing_directory: true,
  },
]
~~~

Retain the detail-envelope consistency route and assert `.workspace-dock-slot .workspace-host-wrapper` becomes visible.

- [ ] **Step 9: Verify the browser workflow**

From `frontend/` run:

~~~bash
node ../node_modules/vite-plus/bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/detail-action-buttons.spec.ts --grep "issue workspace conflict dialog"
~~~

Expected: Chromium and Firefox pass for branch reuse, directory recovery, and new-branch creation.

- [ ] **Step 10: Commit Task 3**

Run context sync and the commit skill. Stage only the component and its two test files. Commit subject:

~~~text
fix: make issue workspace conflicts recoverable
~~~

The body must explain that an already-checked-out orphan branch made reuse appear inert and the dialog now separates checkout from deterministic recovery.

---

### Task 4: Final verification

**Files:**
- Modify only if actual failures require follow-up fixes; never amend earlier commits.

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: verified behavior and a clean generated contract.

- [ ] **Step 1: Run complete affected Go suites**

~~~bash
go test ./internal/workspace ./internal/server/httpapi ./internal/server/workspacetest -shuffle=on
~~~

- [ ] **Step 2: Run the complete frontend unit suite**

~~~bash
./node_modules/.bin/vp test run --project unit
~~~

- [ ] **Step 3: Run frontend checks**

~~~bash
./node_modules/.bin/vp run frontend-check
~~~

- [ ] **Step 4: Run the full affected Playwright file**

From `frontend/`:

~~~bash
node ../node_modules/vite-plus/bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/detail-action-buttons.spec.ts
~~~

- [ ] **Step 5: Verify generated and textual cleanliness**

~~~bash
make api-generate
git diff --check
git status --short
~~~

Expected: regeneration adds no changes and only intended files differ from the task commits.

- [ ] **Step 6: Invoke completion verification**

Use `superpowers:verification-before-completion` and cite actual command results. If a fix is needed, rerun its directly affected checks, then context-sync and create a new commit; never amend.

- [ ] **Step 7: Report outcome**

Summarize the recovery workflow, stable rejection reasons, test results, and commit hashes.
