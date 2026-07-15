# Workspace Upstream Repository Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent workspace pushes from targeting a base-repository branch unless current clone metadata proves the PR head belongs to that repository, while preserving nested GitLab namespaces.

**Architecture:** Encode the approved same/fork/unknown tri-state in the existing nullable `MRHeadRepo` field and refresh it from the current merge-request row before PR setup and agent-context generation. Replace the GitHub-only clone-path parser with provider-aware full-path normalization, then make observer healing trust the current MR row rather than the workspace snapshot.

**Tech Stack:** Go, SQLite-backed workspace fixtures, real temporary Git repositories, testify.

## Global Constraints

- Do not add a database migration or compatibility shim.
- `nil` means confirmed same repository; non-nil empty means unknown; non-nil URL means confirmed fork.
- Branch names and commit SHA equality never authorize an upstream.
- Preserve the complete provider repository path, including GitLab nested namespaces.
- Unknown and fork heads use provider merge-request refs and remain untracked.
- Run direct `go test` commands with `-shuffle=on`, without `-v` or `-count=1`.
- Use testify assertions and existing workspace Git fixtures.

---

### Task 1: Normalize Full Repository Identity And Encode The Tri-State

**Files:**
- Modify: `internal/workspace/monitor.go`
- Modify: `internal/workspace/monitor_test.go`
- Modify: `internal/workspace/manager.go`
- Modify: `internal/workspace/manager_test.go`
- Modify: `internal/workspace/agent_context.go`
- Modify: `internal/db/types.go`

**Interfaces:**
- Produces: `normalizeCloneRepoIdentity(provider, cloneURL string) string`, preserving provider and the full clone path.
- Produces: `workspaceHeadRepo(provider, platformHost, owner, name, cloneURL string) *string`, with the approved tri-state encoding.
- Consumes: existing `normalizeCloneURLHost` and `normalizePlatformHostIdentity` host rules.

- [ ] **Step 1: Add failing nested-path and tri-state tests**

Extend `TestNormalizeCloneRepoIdentity` with:

```go
assert.Equal(
    "gitlab/gitlab.com/group/subgroup/project",
    normalizeCloneRepoIdentity("gitlab", "https://gitlab.com/Group/Subgroup/Project.git"),
)
assert.Equal(
    "gitlab/gitlab.com/group/subgroup/project",
    normalizeCloneRepoIdentity("gitlab", "git@gitlab.com:Group/Subgroup/Project.git"),
)
```

Extend `TestCreatePRHeadRepoClassification` with an `unknown` expectation and a
nested GitLab same-repository case. The unknown assertion must require a
non-nil pointer whose value is empty:

```go
require.NotNil(t, ws.MRHeadRepo)
assert.Empty(*ws.MRHeadRepo)
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/workspace -run 'TestNormalizeCloneRepoIdentity|TestCreatePRHeadRepoClassification' -shuffle=on
```

Expected: nested GitLab identity is truncated to the final `owner/repo`, and
missing metadata still produces `MRHeadRepo == nil`.

- [ ] **Step 3: Implement full path parsing and tri-state classification**

Replace the GitHub-only path parser with a local helper shaped as follows:

```go
func cloneRepoPath(cloneURL string) string {
    var repoPath string
    if strings.Contains(cloneURL, "://") {
        parsed, err := url.Parse(cloneURL)
        if err != nil {
            return ""
        }
        repoPath = parsed.Path
    } else {
        beforePath, path, ok := strings.Cut(cloneURL, ":")
        if !ok || !strings.Contains(beforePath, "@") {
            return ""
        }
        repoPath = path
    }
    repoPath = strings.Trim(strings.TrimSpace(repoPath), "/")
    repoPath = strings.TrimSuffix(repoPath, ".git")
    if strings.Count(repoPath, "/") < 1 {
        return ""
    }
    return repoPath
}
```

Have `normalizeCloneRepoIdentity` combine the provider and normalized host with
this entire path and lowercase only the comparison identity. Thread the
provider through monitor candidate matching and remove the now-unused
`internal/github` import.

Change `workspaceHeadRepo` so empty or unparseable clone metadata returns a
pointer to an empty string, an exact base match returns `nil`, and a different
identity returns a pointer to the original clone URL.

Update `db.Workspace.MRHeadRepo`'s comment with the three states. In
`BuildAgentContext`, populate `ForkHeadRepo` only when the pointer is non-nil
and its trimmed value is non-empty.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run:

```bash
go test ./internal/workspace -run 'TestNormalizeCloneRepoIdentity|TestCreatePRHeadRepoClassification|TestBuildAgentContext' -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit the identity model**

```bash
git add internal/workspace/monitor.go internal/workspace/monitor_test.go internal/workspace/manager.go internal/workspace/manager_test.go internal/workspace/agent_context.go internal/db/types.go
git commit -m "fix: preserve explicit workspace head identity"
```

### Task 2: Reclassify Before Setup And Block Same-SHA Push Redirection

**Files:**
- Modify: `internal/workspace/manager.go`
- Modify: `internal/workspace/manager_test.go`
- Modify: `internal/workspace/agent_context.go`
- Modify: `internal/workspace/agent_context_test.go`
- Modify: `internal/server/api_test.go`

**Interfaces:**
- Produces: `(*Manager).refreshWorkspaceHeadRepo(ctx context.Context, ws *Workspace) error`.
- Consumes: `workspaceHeadRepo` from Task 1 and the current merge-request row.

- [ ] **Step 1: Add failing setup and security regression tests**

Add a table test for `refreshWorkspaceHeadRepo` covering current same-repo,
fork, and missing clone metadata. Seed a persisted PR workspace with the old
`nil` representation, invoke the helper, and assert the refreshed pointer
matches the current MR row.

Add an agent-context test that persists the legacy `nil` representation while
the current MR row has unknown metadata, then verifies launch context does not
advertise an origin push target.

Add `TestAddWorktreeUnknownHeadRepoDoesNotTrackMatchingOriginBranch`:

```go
repoID := seedRepo(t, d, "github.com", "acme", "widget")
seedMRWithHeadRepo(t, d, repoID, prNumber, headBranch, "")
ws, err := mgr.Create(t.Context(), "github", "github.com", "acme", "widget", prNumber)
require.NoError(err)
require.NotNil(ws.MRHeadRepo)

branch, err := mgr.addWorktreeLocked(t.Context(), cloneDir, false, ws)
require.NoError(err)
_, err = gitConfigValue(t.Context(), ws.WorktreePath, "branch."+branch+".remote")
assert.Error(err, "unknown repository identity must leave the branch untracked")
```

The fixture must configure `origin/<head>` and the provider merge-request ref
at the same SHA to reproduce the reported attack precondition.

Add `TestWorkspaceRetryLegacyUnknownHeadRepoLeavesBranchUntrackedE2E` in
`internal/server/api_test.go`. Insert an errored legacy workspace with
`MRHeadRepo == nil`, retry it through the generated HTTP client, wait for setup,
and assert the real Git branch has no `branch.<name>.remote` configuration.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/workspace -run 'TestRefreshWorkspaceHeadRepo|TestAddWorktreeUnknownHeadRepoDoesNotTrackMatchingOriginBranch' -shuffle=on
go test ./internal/server -run '^TestWorkspaceRetryLegacyUnknownHeadRepoLeavesBranchUntrackedE2E$' -shuffle=on
```

Expected: the refresh helper is missing and current creation classifies empty
metadata as same repository before Task 1 is applied; after Task 1, the helper
test remains RED until setup refresh is wired.

- [ ] **Step 3: Reclassify PR workspaces before Git mutation**

Implement:

```go
func (m *Manager) refreshWorkspaceHeadRepo(
    ctx context.Context, ws *Workspace,
) error {
    if ws.ItemType != db.WorkspaceItemTypePullRequest {
        return nil
    }
    repo, err := m.workspaceRepo(ctx, ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName)
    if err != nil {
        return fmt.Errorf("look up workspace repo: %w", err)
    }
    if repo == nil {
        unknown := ""
        ws.MRHeadRepo = &unknown
        return nil
    }
    mr, err := m.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, ws.ItemNumber)
    if err != nil {
        return fmt.Errorf("look up workspace merge request: %w", err)
    }
    cloneURL := ""
    if mr != nil {
        cloneURL = mr.HeadRepoCloneURL
    }
    ws.MRHeadRepo = workspaceHeadRepo(
        ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName, cloneURL,
    )
    return nil
}
```

Call it at the beginning of `SetupWithWorktreeBasePath`, before existing
worktree provenance or clone selection. Unknown becomes fork-safe because the
existing setup decision tree treats any non-nil `MRHeadRepo` as requiring the
provider merge-request ref and disallowing local-base reuse.

Update the fallback-upstream comment: confirmed same-repository identity is the
authorization; SHA equality is only an additional checkout-consistency check.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run:

```bash
go test ./internal/workspace -run 'TestRefreshWorkspaceHeadRepo|TestAddWorktreeUnknownHeadRepoDoesNotTrackMatchingOriginBranch|TestAddWorktreeFallbackBranchTracksPRHeadBranch' -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit the safe setup routing**

```bash
git add internal/workspace/manager.go internal/workspace/manager_test.go
git commit -m "fix: require repository identity before workspace tracking"
```

### Task 3: Heal From Current Nested Repository Metadata

**Files:**
- Modify: `internal/workspace/pushed_head_observer.go`
- Modify: `internal/workspace/pushed_head_observer_test.go`

**Interfaces:**
- Consumes: provider-neutral full-path normalization from Task 1.
- Produces: observer healing based solely on the current MR row's explicit same-repository evidence.

- [ ] **Step 1: Add failing observer regression cases**

Extend `TestPushedHeadObserverUpstreamHeal` so a workspace carrying an empty
non-nil historical `MRHeadRepo` heals when the current MR row has a confirmed
same-repository clone URL.

Add a nested GitLab case with:

```go
platformHost := "gitlab.com"
owner := "group/subgroup"
name := "project"
headRepoCloneURL := "https://gitlab.com/group/subgroup/project.git"
```

Assert one `SetBranchUpstream` call. Add a direct table test of
`configureMissingUpstream` for provider-issue and Kata-task workspaces whose
refresh-mapped associated PR has confirmed same-repository metadata.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/workspace -run 'TestPushedHeadObserverUpstreamHeal|TestConfigureMissingUpstreamForRefreshMappedWorkspaces' -shuffle=on
```

Expected: nested GitLab comparison fails with the truncated path, and a
workspace's non-nil unknown snapshot blocks healing before current-row
classification is evaluated.

- [ ] **Step 3: Make the current MR row authoritative**

Remove the early `ws.MRHeadRepo != nil` rejection from
`configureMissingUpstream`. Retain the current-row gate:

```go
if strings.TrimSpace(mr.HeadRepoCloneURL) == "" || workspaceHeadRepo(
    ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName, mr.HeadRepoCloneURL,
) != nil {
    return false, nil
}
```

This permits historical unknown/fork snapshots to heal only after refresh
proves the current head is in the base repository. Fork and unknown current
rows remain untracked.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run:

```bash
go test ./internal/workspace -run 'TestPushedHeadObserverUpstreamHeal|TestConfigureMissingUpstreamForRefreshMappedWorkspaces' -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit observer healing**

```bash
git add internal/workspace/pushed_head_observer.go internal/workspace/pushed_head_observer_test.go
git commit -m "fix: heal workspace upstreams from current repo identity"
```

### Task 4: Verify The Complete Workspace Behavior

**Files:**
- Review: `context/workspace-apis.md`
- Review: `docs/superpowers/specs/2026-07-15-workspace-upstream-repository-identity-design.md`
- Review: all files modified by Tasks 1-3

**Interfaces:**
- Consumes: all preceding tasks.
- Produces: verified security and nested-namespace behavior with no unrelated diff.

- [ ] **Step 1: Format modified Go files**

Run `gofmt -w` on the exact Go files changed by Tasks 1-3.

- [ ] **Step 2: Run the full workspace package suite**

```bash
go test ./internal/workspace -shuffle=on
```

Expected: PASS.

- [ ] **Step 3: Run the short repository suite**

```bash
make test-short
```

Expected: PASS.

- [ ] **Step 4: Review the final diff and context**

Run:

```bash
git diff --check
git status --short
scripts/context-sync --check
```

Confirm the existing Branch Upstream context states that SHA equality is not
repository identity evidence. Update it only if implementation changes the
approved invariant.

- [ ] **Step 5: Commit any verification-driven correction**

If formatting or verification required a code correction, stage only those
files and create a new conventional commit. Do not amend any prior commit.
