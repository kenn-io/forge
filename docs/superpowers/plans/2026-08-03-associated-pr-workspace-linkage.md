# Associated PR Workspace Linkage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make pull detail return a directly owned workspace or, when none exists, the deterministic newest workspace with a persisted association to that pull request.

**Architecture:** Add a new database query for read-only pull-detail linkage and route the workspace manager's pull lookup through it. Keep the existing direct-PR-only database query unchanged for merge-request sync's head-repository trust writes, and prove that separation at the database, sync, and HTTP boundaries.

**Tech Stack:** Go, SQLite, Huma-generated API client tests, Testify.

## Global Constraints

- Direct PR workspaces always outrank later-associated issue, Kata, or ad-hoc workspaces.
- Associated candidates sort by `created_at DESC, id DESC`; ID order is stable but does not represent age.
- Workspace status does not affect selection; the selected `{ id, status }` reference reports lifecycle state.
- `internal/db/queries.go::DB.GetWorkspaceByMRForProvider` remains direct-PR-only for merge-request sync reclassification.
- Preserve provider normalization, case-folded repository routes, and historical-route collision fail-closed behavior.
- Do not infer associations from branch names on the read path or add a compatibility adapter.
- Do not change the API schema, generated clients, Svelte components, repository route-reuse behavior, or workspace lifecycle behavior.
- Run branch code only against test-owned temporary state; do not inspect or migrate the production database.

---

### Task 1: Add a pull-detail-specific workspace lookup without widening sync writes

**Files:**
- Modify: `internal/db/queries_test.go`
- Modify: `internal/db/repository_catalog_test.go`
- Modify: `internal/db/queries.go`
- Modify: `internal/github/sync_test.go`

**Interfaces:**
- Consumes: `canonicalRepoLookupIdentifier`, `workspaceRouteHasHistoricalOccupants`, `scanWorkspace`, and `forge_workspaces.associated_pr_number`.
- Produces: `func (d *DB) GetWorkspaceLinkedToMRForProvider(ctx context.Context, provider, platformHost, owner, name string, mrNumber int) (*Workspace, error)`.
- Preserves: `func (d *DB) GetWorkspaceByMRForProvider(...) (*Workspace, error)` as the direct-PR-only lookup used by `Syncer.reclassifyWorkspaceHeadRepoTrustUnderRepositoryReconciliationRead`.

- [ ] **Step 1: Add fixture helpers and the failing selection regression**

In `internal/db/queries_test.go`, add focused helpers:

```go
func workspaceLinkageTestDB(t *testing.T) *DB {
	t.Helper()
	d := openTestDB(t)
	_, err := d.UpsertRepo(t.Context(), RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget",
	})
	require.NoError(t, err)
	return d
}

func insertWorkspaceLinkageFixture(t *testing.T, d *DB, ws Workspace, createdAt time.Time) {
	t.Helper()
	require.NoError(t, d.InsertWorkspace(t.Context(), &ws))
	_, err := d.WriteDB().ExecContext(
		t.Context(), `UPDATE forge_workspaces SET created_at = ? WHERE id = ?`,
		createdAt.UTC(), ws.ID,
	)
	require.NoError(t, err)
}
```

Add `TestGetWorkspaceLinkedToMRForProviderSelection` with these literal
fixtures; every row uses provider `github`, host `github.com`, repository
`acme/widget`, and PR number 42:

| Subtest | ID | Item type/key | Association | Created/status | Expected |
| --- | --- | --- | --- | --- | --- |
| fallback | `associated-issue` | issue 7 | PR 42 | `base`, ready | linked lookup returns this; direct-only lookup returns nil |
| ordering | `associated-ready` | issue 7 | PR 42 | `base`, ready | loses to newer candidates |
| ordering | `associated-error` | ad-hoc key from ID | PR 42 | `base+1m`, error | loses the ID tie |
| ordering | `associated-z-creating` | ad-hoc key from ID | PR 42 | `base+1m`, creating | selected despite status |
| precedence | `new-associated` | issue 7 | PR 42 | `base+1h`, ready | loses to direct ownership |
| precedence | `old-direct` | direct PR 42 | none | `base`, ready | selected despite age |

In the fallback subtest, call the new lookup with `GITHUB`, `GITHUB.COM`,
`ACME`, and `WIDGET` so the test also proves the retained normalization and
case-folding behavior.

Use complete workspace fixtures of this form, changing IDs, item types, keys, timestamps, and statuses for each case:

```go
associatedPR := 42
insertWorkspaceLinkageFixture(t, d, Workspace{
	ID: "associated-issue", Platform: "github", PlatformHost: "github.com",
	RepoOwner: "acme", RepoName: "widget", ItemType: WorkspaceItemTypeIssue,
	ItemNumber: 7, AssociatedPRNumber: &associatedPR, GitHeadRef: "issue-7",
	WorkspaceBranch: "issue-7", WorktreePath: "/tmp/associated-issue",
	TmuxSession: "associated-issue", Status: "ready",
}, base)

linked, err := d.GetWorkspaceLinkedToMRForProvider(
	t.Context(), "github", "github.com", "acme", "widget", associatedPR,
)
require.NoError(t, err)
require.NotNil(t, linked)
assert.Equal(t, "associated-issue", linked.ID)
```

For ad-hoc candidates use `ItemNumber: 0` and `ItemKey: AdHocWorkspaceItemKey(id)`. For the direct candidate use `ItemType: WorkspaceItemTypePullRequest`, `ItemNumber: associatedPR`, and no `AssociatedPRNumber`.

- [ ] **Step 2: Run the database test and verify RED**

Run:

```bash
go test ./internal/db -run TestGetWorkspaceLinkedToMRForProviderSelection -shuffle=on
```

Expected: compilation fails because `GetWorkspaceLinkedToMRForProvider` does not exist.

- [ ] **Step 3: Implement the dedicated database query**

In `internal/db/queries.go`, add this method beside `GetWorkspaceByMRForProvider` without changing the existing method or `getWorkspaceByMR`:

```go
// GetWorkspaceLinkedToMRForProvider returns the workspace represented by a
// pull detail: a direct PR workspace first, then the newest workspace with a
// persisted PR association.
func (d *DB) GetWorkspaceLinkedToMRForProvider(
	ctx context.Context,
	provider, platformHost, owner, name string,
	mrNumber int,
) (*Workspace, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	platformHost, owner, name = canonicalRepoLookupIdentifier(platformHost, owner, name)
	collision, err := d.workspaceRouteHasHistoricalOccupants(
		ctx, provider, platformHost, owner+"/"+name,
	)
	if err != nil {
		return nil, err
	}
	if collision {
		return nil, nil
	}

	ws, err := scanWorkspace(d.ro.QueryRowContext(ctx, `
		SELECT id, platform, platform_host, repo_owner, repo_name,
		       item_type, item_number, item_key, associated_pr_number,
		       git_head_ref, mr_head_repo, workspace_branch,
		       worktree_path, tmux_session, terminal_backend, status,
		       error_message, created_at, kata_metadata
		FROM forge_workspaces
		WHERE platform_host = ? AND repo_owner_key = ? AND repo_name_key = ?
		  AND (
		      (item_type = ? AND item_number = ?)
		      OR
		      (item_type IN (?, ?, ?) AND associated_pr_number = ?)
		  )
		  AND (? = '' OR platform = ?)
		ORDER BY
		  CASE WHEN item_type = ? AND item_number = ? THEN 0 ELSE 1 END,
		  created_at DESC,
		  id DESC
		LIMIT 1`,
		platformHost, owner, name,
		WorkspaceItemTypePullRequest, mrNumber,
		WorkspaceItemTypeIssue, WorkspaceItemTypeKataTask, WorkspaceItemTypeAdHoc, mrNumber,
		provider, provider,
		WorkspaceItemTypePullRequest, mrNumber,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace linked to MR: %w", err)
	}
	return ws, nil
}
```

Do not redirect `GetWorkspaceByMRForProvider` to this method; sync relies on the narrower query.

- [ ] **Step 4: Run database selection tests and verify GREEN**

Run:

```bash
go test ./internal/db -run 'TestGetWorkspaceLinkedToMRForProviderSelection|TestGetWorkspaceByMRForProviderDisambiguatesProvider' -shuffle=on
```

Expected: all selected database tests pass, including the direct-only isolation assertion.

- [ ] **Step 5: Add the sync-side characterization guard**

In `internal/github/sync_test.go`, add this regression beside the existing reclassification tests:

```go
func TestReclassifyWorkspaceHeadRepoTrustIgnoresAssociatedWorkspace(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	repoID, err := d.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 1001, PlatformExternalID: "mr-1", Number: 1,
		Title: "Fork PR", State: db.MergeRequestStateOpen,
		HeadBranch: "feature", BaseBranch: "main",
		HeadRepoCloneURL: "https://github.com/new-fork/repo.git",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	associatedPR := 1
	originalHeadRepo := "https://github.com/original-fork/repo.git"
	ws := &db.Workspace{
		ID: "associated-only", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "owner", RepoName: "repo", ItemType: db.WorkspaceItemTypeAdHoc,
		ItemNumber: 0, ItemKey: db.AdHocWorkspaceItemKey("feature"),
		AssociatedPRNumber: &associatedPR, GitHeadRef: "feature",
		MRHeadRepo: &originalHeadRepo, WorktreePath: t.TempDir(), Status: "ready",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	syncer := &Syncer{db: d}
	syncer.reclassifyWorkspaceHeadRepoTrust(ctx, RepoRef{
		Owner: "owner", Name: "repo", PlatformHost: "github.com",
	}, repoID, associatedPR)

	stored, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	require.NotNil(stored.MRHeadRepo)
	assert.Equal(originalHeadRepo, *stored.MRHeadRepo)
}
```

- [ ] **Step 6: Extend the route-reuse fail-closed guard**

In `internal/db/repository_catalog_test.go`, extend
`TestWorkspaceRouteLookupAndCreationFailClosedAfterReplacement` after its
existing direct lookup assertion:

```go
linkedByRoute, err := d.GetWorkspaceLinkedToMRForProvider(
	t.Context(), "github", "github.com", "org-a", "project-a", 7,
)
require.NoError(err)
assert.Nil(linkedByRoute)
```

This proves the new query cannot recover a workspace through a reused route.

- [ ] **Step 7: Run the sync and route-safety guards**

Run:

```bash
go test ./internal/github -run 'TestReclassifyWorkspaceHeadRepoTrust(IgnoresAssociatedWorkspace|RetriesAfterRevisionChange)' -shuffle=on
go test ./internal/db -run TestWorkspaceRouteLookupAndCreationFailClosedAfterReplacement -shuffle=on
```

Expected: all tests pass. The sync test is a characterization guard rather
than a RED test because sync behavior must not change; switching sync to the
new linkage lookup must make it fail.

- [ ] **Step 8: Format, context-sync, and commit Task 1**

Run:

```bash
gofmt -w internal/db/queries.go internal/db/queries_test.go internal/db/repository_catalog_test.go internal/github/sync_test.go
git diff --check
```

Then read and follow `.agents/skills/context-sync/SKILL.md` in `--commit` mode and the mandatory `commit` skill. Stage only the four Task 1 files and commit with:

```bash
git commit -m "feat: resolve persisted PR workspace associations" \
  -m "Pull detail needs a deterministic associated-workspace fallback, but merge-request sync must keep its direct-only write target. Add a separate lookup so read presentation can broaden without changing head-repository trust classification." \
  -m $'Generated with Codex\nCo-authored-by: Codex <noreply@openai.com>'
```

### Task 2: Wire pull detail to the associated-workspace lookup

**Files:**
- Modify: `internal/server/api_test.go`
- Modify: `internal/workspace/manager.go`
- Modify: `context/workspace-apis.md`

**Interfaces:**
- Consumes: `DB.GetWorkspaceLinkedToMRForProvider(...)` from Task 1 and `pullapi.MergeRequestDetailResponse.Workspace`.
- Produces: `Manager.GetByMRForProvider(...)` returning the direct-or-associated selection for pull-detail reads.
- Preserves: the pull API's best-effort `{ id, status }` conversion and every generated API type.

- [ ] **Step 1: Write the failing pull-detail API regression**

In `internal/server/api_test.go`, add this test near `TestAPIGetPullDetailLoaded`. It uses the isolated workspace server fixture because an ordinary test server intentionally has no workspace manager:

```go
func TestAPIGetPullDetailIncludesAssociatedWorkspace(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	client, database, _, _ := setupTestServerWithWorkspaces(t)
	associatedPR := 1

	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "bug-merge-methods",
		Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypeAdHoc, ItemNumber: 0,
		ItemKey: db.AdHocWorkspaceItemKey("bug-merge-methods"),
		AssociatedPRNumber: &associatedPR,
		GitHeadRef: "bug-merge-methods", WorkspaceBranch: "bug-merge-methods",
		WorktreePath: filepath.Join(t.TempDir(), "bug-merge-methods"),
		TmuxSession: "bug-merge-methods", Status: "ready",
	}))

	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", int64(associatedPR),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Workspace)
	assert.Equal("bug-merge-methods", resp.JSON200.Workspace.Id)
	assert.Equal("ready", resp.JSON200.Workspace.Status)
}
```

- [ ] **Step 2: Run the API test and verify RED**

Run:

```bash
go test ./internal/server -run TestAPIGetPullDetailIncludesAssociatedWorkspace -shuffle=on
```

Expected: the response succeeds but `Workspace` is nil because the manager still delegates to the direct-only database lookup.

- [ ] **Step 3: Route the manager read through the new lookup**

In `internal/workspace/manager.go`, keep provider normalization and change only the final database call in `GetByMRForProvider`:

```go
return m.db.GetWorkspaceLinkedToMRForProvider(
	ctx, string(kind), platformHost, owner, name, mrNumber,
)
```

Do not change `internal/github/sync.go`; its direct database call is the intentional write boundary.

- [ ] **Step 4: Run the API test and verify GREEN**

Run:

```bash
go test ./internal/server -run TestAPIGetPullDetailIncludesAssociatedWorkspace -shuffle=on
```

Expected: the detail response contains workspace ID `bug-merge-methods` with status `ready`.

- [ ] **Step 5: Record the implemented selection invariant**

In `context/workspace-apis.md`, add two terse anchored bullets in Data Model Intent:

```markdown
- Pull detail prefers the direct PR workspace, then the newest persisted issue,
  Kata, or ad-hoc PR association by creation time and stable ID; status never
  changes selection (`internal/db/queries.go::DB.GetWorkspaceLinkedToMRForProvider`).
- Merge-request sync limits head-repository trust writes to direct PR workspaces;
  associated rows are presentation links, not sync write targets
  (`internal/github/sync.go::Syncer.reclassifyWorkspaceHeadRepoTrustUnderRepositoryReconciliationRead`).
```

- [ ] **Step 6: Format and run all focused regressions together**

Run:

```bash
gofmt -w internal/workspace/manager.go internal/server/api_test.go
go test ./internal/db -run 'TestGetWorkspaceLinkedToMRForProviderSelection|TestGetWorkspaceByMRForProviderDisambiguatesProvider' -shuffle=on
go test ./internal/db -run TestWorkspaceRouteLookupAndCreationFailClosedAfterReplacement -shuffle=on
go test ./internal/github -run 'TestReclassifyWorkspaceHeadRepoTrust(IgnoresAssociatedWorkspace|RetriesAfterRevisionChange)' -shuffle=on
go test ./internal/server -run TestAPIGetPullDetailIncludesAssociatedWorkspace -shuffle=on
git diff --check
```

Expected: all focused tests and whitespace checks pass.

- [ ] **Step 7: Run the affected backend suites**

Run:

```bash
go test ./internal/db ./internal/workspace ./internal/github ./internal/server -shuffle=on
```

Expected: all four affected package suites pass. No frontend suite or API generation is required because the wire schema and Svelte code are unchanged.

- [ ] **Step 8: Verify the implementation against the design**

Confirm all of these from the final diff and fresh test output:

- direct workspace precedence is enforced;
- associated fallback is limited to issue, Kata, and ad-hoc item types;
- creation time, stable ID, and status-independent selection are tested;
- `internal/github/sync.go` still calls `GetWorkspaceByMRForProvider` directly;
- pull detail returns the existing `{ id, status }` field;
- no migration, generated API artifact, or frontend file changed.

- [ ] **Step 9: Context-sync and commit Task 2**

Read and follow `.agents/skills/context-sync/SKILL.md` in `--commit` mode and the mandatory `commit` skill. Stage only the Task 2 files and commit with:

```bash
git commit -m "fix: expose associated workspaces in pull detail" \
  -m "Activity could not claim a workspace created before its pull request because detail only recognized direct PR ownership. Route pull-detail reads through the persisted-association lookup while leaving merge-request sync's direct-only trust writes unchanged." \
  -m $'Generated with Codex\nCo-authored-by: Codex <noreply@openai.com>'
```
