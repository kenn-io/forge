# Workspace Item Activity Sort Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a workspace list sort option that orders workspaces by the underlying PR or issue `last_activity_at`, falling back to workspace `created_at` when no synced PR/issue exists.

**Architecture:** The database already stores `last_activity_at` on `middleman_merge_requests` and `middleman_issues`. Expose the item-owned timestamp through `WorkspaceSummary` and `workspaceResponse`, regenerate API clients, then add a Svelte-side flat sort mode that uses the new JSON field. Keep the existing `Activity` sort as terminal/tmux activity to avoid changing current behavior.

**Tech Stack:** Go, SQLite, Huma/OpenAPI, generated Go and TypeScript API clients, Svelte 5, Vite+ tests.

**Success Criteria:**
- `Item activity` appears as an opt-in flat sort option in the workspace sort menu.
- PR workspaces sort by the synced PR `last_activity_at`; issue workspaces sort by the synced issue `last_activity_at`.
- Workspaces without synced owner item metadata fall back to workspace `created_at`.
- Existing `Activity` sorting still means terminal/tmux output activity, and grouped `Org / repo` ordering is unchanged.

**Non-Goals:**
- Do not change provider sync semantics or write new provider-specific item freshness logic.
- Do not change existing `Activity` sort behavior or label.
- Do not add a compatibility alias for old sort values.

---

## File Structure

- Modify `internal/db/types.go`
  - Add `ItemLastActivityAt *time.Time` to `WorkspaceSummary`.
- Modify `internal/db/queries.go`
  - Join `m.last_activity_at` for PR workspaces and `i.last_activity_at` for issue workspaces into the shared workspace summary SELECT.
  - Normalize the timestamp to UTC in `scanWorkspaceSummary`.
- Modify `internal/db/queries_test.go`
  - Extend `TestWorkspaceSummaries` to assert PR and issue item activity timestamps, plus nil for a workspace without synced owner item metadata.
- Modify `internal/server/api_types.go`
  - Add `ItemLastActivityAt *string json:"item_last_activity_at,omitempty"` to `workspaceResponse`.
  - Map `WorkspaceSummary.ItemLastActivityAt` to UTC RFC3339.
- Modify `internal/server/api_test.go`
  - Add a wire-level `/api/v1/workspaces` assertion that the API returns `item_last_activity_at` for both PR and issue-backed workspaces and omits it when no owner item is synced.
- Regenerate generated API artifacts with `make api-generate`
  - Expected generated files include `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json`, `internal/apiclient/generated/client.gen.go`, and `packages/ui/src/api/generated/schema.ts`.
- Modify `frontend/src/lib/components/terminal/workspaceListSort.ts`
  - Extend `WorkspaceListSort` with a new value, recommended label `Item activity`.
- Modify `frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`
  - Add `item_last_activity_at?: string | null` to the local `Workspace` interface.
  - Extend `sortedFlat` so the new sort uses `item_last_activity_at || created_at`.
  - Keep the existing `activity` mode using `tmux_last_output_at || created_at`.
- Modify `frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts`
  - Extend fixtures with `itemLastActivityAt`.
  - Add a test for the new sort option and fallback behavior.
- Modify `frontend/tests/e2e-full/00-workspace-sidebar.spec.ts`
  - Add full-stack coverage for selecting and persisting `Item activity` against real workspace API data.

## Tasks

### Task 1: Expose Item Last Activity From Workspace Summaries

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries.go`
- Test: `internal/db/queries_test.go`

- [ ] **Step 1: Write the failing DB summary assertions**

In `internal/db/queries_test.go`, inside `TestWorkspaceSummaries`, use distinct PR and issue activity timestamps:

```go
issueActivity := base.Add(3 * time.Hour)
prActivity := base.Add(2 * time.Hour)

insertTestIssue(
	t, d, repoID, 7,
	"Track workspace association",
	issueActivity,
)
_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
	RepoID:         repoID,
	PlatformID:     5001,
	Number:         42,
	URL:            "https://github.com/acme/widget/pull/42",
	Title:          "Add workspace support",
	Author:         "alice",
	State:          "open",
	IsDraft:        true,
	CIStatus:       "pending",
	ReviewDecision: "REVIEW_REQUIRED",
	Additions:      100,
	Deletions:      20,
	CreatedAt:      base,
	UpdatedAt:      base.Add(time.Hour),
	LastActivityAt: prActivity,
})
require.NoError(err)
```

Then add these assertions after the existing nil metadata checks:

```go
assert.Nil(noMR.ItemLastActivityAt)

require.NotNil(issueWithPR.ItemLastActivityAt)
assert.Equal(issueActivity.UTC(), issueWithPR.ItemLastActivityAt.UTC())

require.NotNil(withMR.ItemLastActivityAt)
assert.Equal(prActivity.UTC(), withMR.ItemLastActivityAt.UTC())
```

Also add this assertion in the `GetWorkspaceSummary` section:

```go
require.NotNil(single.ItemLastActivityAt)
assert.Equal(issueActivity.UTC(), single.ItemLastActivityAt.UTC())
```

- [ ] **Step 2: Run the DB test to verify it fails**

Run:

```bash
go test ./internal/db -run TestWorkspaceSummaries -shuffle=on
```

Expected: FAIL because `WorkspaceSummary` does not yet have `ItemLastActivityAt`.

- [ ] **Step 3: Add the summary field**

In `internal/db/types.go`, update `WorkspaceSummary`:

```go
type WorkspaceSummary struct {
	Workspace
	MRTitle            *string
	MRState            *string
	MRIsDraft          *bool
	MRCIStatus         *string
	MRReviewDecision   *string
	MRAdditions        *int
	MRDeletions        *int
	ItemLastActivityAt *time.Time
}
```

- [ ] **Step 4: Populate the summary field from the joined owner item**

In `internal/db/queries.go`, extend `workspaceSummaryColumns` after `m.deletions`:

```go
	m.review_decision, m.additions, m.deletions,
	CASE
	    WHEN w.item_type = 'issue' THEN i.last_activity_at
	    ELSE m.last_activity_at
	END`
```

Update `scanWorkspaceSummary`:

```go
		&s.MRTitle, &s.MRState, &s.MRIsDraft, &s.MRCIStatus,
		&s.MRReviewDecision, &s.MRAdditions, &s.MRDeletions,
		&s.ItemLastActivityAt,
```

Normalize it after `s.CreatedAt = s.CreatedAt.UTC()`:

```go
	if s.ItemLastActivityAt != nil {
		utc := s.ItemLastActivityAt.UTC()
		s.ItemLastActivityAt = &utc
	}
```

- [ ] **Step 5: Run the DB test to verify it passes**

Run:

```bash
go test ./internal/db -run TestWorkspaceSummaries -shuffle=on
```

Expected: PASS.

### Task 2: Add `item_last_activity_at` to the Workspace API

**Files:**
- Modify: `internal/server/api_types.go`
- Test: `internal/server/api_test.go`
- Generate: `frontend/openapi/openapi.yaml`
- Generate: `internal/apiclient/spec/openapi.json`
- Generate: `internal/apiclient/generated/client.gen.go`
- Generate: `packages/ui/src/api/generated/schema.ts`

- [ ] **Step 1: Write the failing API assertion**

Add a focused test near the existing workspace API tests in `internal/server/api_test.go`:

```go
func TestListWorkspacesIncludesItemLastActivityAt(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	client, database, _, _ := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	repo, err := database.GetRepoByHostOwnerName(ctx, "github.com", "acme", "widget")
	require.NoError(err)
	require.NotNil(repo)

	base := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	prActivity := base.Add(2 * time.Hour)
	issueActivity := base.Add(3 * time.Hour)

	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repo.ID,
		PlatformID:     9001,
		Number:         701,
		URL:            "https://github.com/acme/widget/pull/701",
		Title:          "Sort PR workspace",
		Author:         "alice",
		State:          "open",
		CreatedAt:      base,
		UpdatedAt:      base.Add(time.Hour),
		LastActivityAt: prActivity,
	})
	require.NoError(err)

	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repo.ID,
		PlatformID:     9002,
		Number:         702,
		URL:            "https://github.com/acme/widget/issues/702",
		Title:          "Sort issue workspace",
		Author:         "bob",
		State:          "open",
		CreatedAt:      base,
		UpdatedAt:      base.Add(time.Hour),
		LastActivityAt: issueActivity,
	})
	require.NoError(err)

	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "ws-pr-activity",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   701,
		GitHeadRef:   "feature/pr-activity",
		WorktreePath: filepath.Join(t.TempDir(), "ws-pr-activity"),
		TmuxSession:  "middleman-ws-pr-activity",
		Status:       "creating",
		CreatedAt:    base,
	}))
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "ws-issue-activity",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypeIssue,
		ItemNumber:   702,
		GitHeadRef:   "feature/issue-activity",
		WorktreePath: filepath.Join(t.TempDir(), "ws-issue-activity"),
		TmuxSession:  "middleman-ws-issue-activity",
		Status:       "creating",
		CreatedAt:    base.Add(time.Minute),
	}))
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "ws-unsynced-activity",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypeIssue,
		ItemNumber:   799,
		GitHeadRef:   "feature/unsynced-activity",
		WorktreePath: filepath.Join(t.TempDir(), "ws-unsynced-activity"),
		TmuxSession:  "middleman-ws-unsynced-activity",
		Status:       "creating",
		CreatedAt:    base.Add(2 * time.Minute),
	}))

	resp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Workspaces)

	byID := map[string]generated.WorkspaceResponse{}
	for _, ws := range *resp.JSON200.Workspaces {
		byID[ws.Id] = ws
	}

	require.Contains(byID, "ws-pr-activity")
	require.Contains(byID, "ws-issue-activity")
	require.Contains(byID, "ws-unsynced-activity")

	assert.NotNil(byID["ws-pr-activity"].ItemLastActivityAt)
	assert.Equal(prActivity.Format(time.RFC3339), *byID["ws-pr-activity"].ItemLastActivityAt)
	assert.NotNil(byID["ws-issue-activity"].ItemLastActivityAt)
	assert.Equal(issueActivity.Format(time.RFC3339), *byID["ws-issue-activity"].ItemLastActivityAt)
	assert.Nil(byID["ws-unsynced-activity"].ItemLastActivityAt)
}
```

- [ ] **Step 2: Run the API test to verify it fails**

Run:

```bash
go test ./internal/server -run TestListWorkspacesIncludesItemLastActivityAt -shuffle=on
```

Expected: FAIL because `WorkspaceResponse.ItemLastActivityAt` is not generated or mapped yet.

- [ ] **Step 3: Add the response field and mapper**

In `internal/server/api_types.go`, add the field after `CreatedAt`:

```go
	ItemLastActivityAt *string         `json:"item_last_activity_at,omitempty"`
```

In `toWorkspaceResponse`, format the pointer:

```go
	var itemLastActivityAt *string
	if s.ItemLastActivityAt != nil {
		formatted := s.ItemLastActivityAt.UTC().Format(time.RFC3339)
		itemLastActivityAt = &formatted
	}
```

Then include it in the returned `workspaceResponse`:

```go
		ItemLastActivityAt: itemLastActivityAt,
```

- [ ] **Step 4: Regenerate API artifacts**

Run:

```bash
make api-generate
```

Expected: generated OpenAPI and API client files update with `item_last_activity_at`.

- [ ] **Step 5: Run the API test to verify it passes**

Run:

```bash
go test ./internal/server -run TestListWorkspacesIncludesItemLastActivityAt -shuffle=on
```

Expected: PASS.

### Task 3: Add the Workspace List Sort Option

**Files:**
- Modify: `frontend/src/lib/components/terminal/workspaceListSort.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`
- Test: `frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts`

- [ ] **Step 1: Write the failing frontend test**

In `frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts`, extend `WorkspaceFixtureOptions`:

```ts
  itemLastActivityAt?: string | null;
```

Extend `workspaceFixture` parameters and returned object:

```ts
  itemLastActivityAt = null,
}: WorkspaceFixtureOptions) {
  return {
    // existing fields...
    item_last_activity_at: itemLastActivityAt,
  };
}
```

Add this test:

```ts
  it("sorts flat by item activity with creation time as fallback", async () => {
    mockGet.mockResolvedValue({
      data: {
        workspaces: [
          workspaceFixture({
            id: "ws-created-newest",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 1,
            title: "Newest created fallback",
            createdAt: "2026-05-15T12:00:00Z",
          }),
          workspaceFixture({
            id: "ws-pr-active",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "middleman",
            number: 2,
            title: "PR recently changed",
            createdAt: "2026-05-10T12:00:00Z",
            itemLastActivityAt: "2026-05-16T12:00:00Z",
          }),
          workspaceFixture({
            id: "ws-issue-active",
            provider: "github",
            platformHost: "github.com",
            owner: "kenn-io",
            name: "agentsview",
            number: 3,
            title: "Issue recently changed",
            itemType: "issue",
            createdAt: "2026-05-09T12:00:00Z",
            itemLastActivityAt: "2026-05-17T12:00:00Z",
          }),
        ],
      },
    });

    const { container } = render(WorkspaceListSidebar, {
      props: { selectedId: "ws-created-newest" },
    });
    await screen.findByText("Newest created fallback");

    await fireEvent.click(screen.getByTitle("Sort workspaces"));
    await fireEvent.click(screen.getByRole("button", { name: "Item activity" }));

    expect(rowTitles(container)).toEqual([
      "Issue recently changed",
      "PR recently changed",
      "Newest created fallback",
    ]);
    expect(container.querySelectorAll(".group-header")).toHaveLength(0);
  });
```

- [ ] **Step 2: Run the frontend test to verify it fails**

Run:

```bash
./node_modules/.bin/vp run test -- WorkspaceListSidebar.test.ts
```

Expected: FAIL because the menu does not include `Item activity`.

- [ ] **Step 3: Add the sort option**

In `frontend/src/lib/components/terminal/workspaceListSort.ts`:

```ts
export type WorkspaceListSort = "repo" | "created" | "activity" | "item-activity";
```

Update `workspaceListSortOptions`:

```ts
export const workspaceListSortOptions: {
  value: WorkspaceListSort;
  label: string;
}[] = [
  { value: "repo", label: "Org / repo" },
  { value: "created", label: "Created" },
  { value: "activity", label: "Activity" },
  { value: "item-activity", label: "Item activity" },
];
```

- [ ] **Step 4: Wire the new timestamp into the Svelte component**

In `frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`, add to the local `Workspace` interface:

```ts
    item_last_activity_at?: string | null;
```

Replace `sortedFlat` with:

```ts
  const sortedFlat = $derived.by(() => {
    const stamp =
      sortMode === "activity"
        ? (ws: Workspace) =>
          timeValue(ws.tmux_last_output_at) || timeValue(ws.created_at)
        : sortMode === "item-activity"
          ? (ws: Workspace) =>
            timeValue(ws.item_last_activity_at) || timeValue(ws.created_at)
          : (ws: Workspace) => timeValue(ws.created_at);
    return [...visibleWorkspaces].sort(
      (a, b) => stamp(b) - stamp(a) || a.id.localeCompare(b.id),
    );
  });
```

Update the existing comment above `sortedFlat`:

```ts
  // Flat ordering for timestamp sorts. The org/repo mode keeps
  // the API order (created_at DESC) inside each repo group.
  // "Activity" means terminal output only (tmux_last_output_at).
  // "Item activity" means the synced PR/issue last_activity_at.
  // Missing timestamps fall back to workspace creation time.
```

- [ ] **Step 5: Run Svelte autofixer on the edited component**

Run:

```bash
vp exec svelte-mcp svelte-autofixer frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte --svelte-version 5
```

Expected: no required fixes.

- [ ] **Step 6: Run the frontend test to verify it passes**

Run:

```bash
./node_modules/.bin/vp run test -- WorkspaceListSidebar.test.ts
```

Expected: PASS.

### Task 4: Add Full-Stack Workspace Sidebar Coverage

**Files:**
- Test: `frontend/tests/e2e-full/00-workspace-sidebar.spec.ts`

- [ ] **Step 1: Extend the existing flat sort e2e test**

In `frontend/tests/e2e-full/00-workspace-sidebar.spec.ts`, extend `flat sort modes order real workspaces by creation time and keep provider identity` so it fetches `/api/v1/workspaces`, derives the expected descending order from `item_last_activity_at || created_at`, selects `Item activity`, and asserts the visible flat row order plus reload persistence.

- [ ] **Step 2: Run the affected e2e spec**

Run:

```bash
cd frontend && node ./scripts/run-e2e-to-file.ts --project=chromium tests/e2e-full/00-workspace-sidebar.spec.ts
```

Expected: PASS.

### Task 5: Full Verification and Commit

**Files:**
- Verify all modified files.
- Commit all final changes.

- [ ] **Step 1: Run focused backend tests**

Run:

```bash
go test ./internal/db -run TestWorkspaceSummaries -shuffle=on
go test ./internal/server -run TestListWorkspacesIncludesItemLastActivityAt -shuffle=on
```

Expected: both PASS.

- [ ] **Step 2: Run focused frontend tests**

Run:

```bash
./node_modules/.bin/vp run test -- WorkspaceListSidebar.test.ts
cd frontend && node ./scripts/run-e2e-to-file.ts --project=chromium tests/e2e-full/00-workspace-sidebar.spec.ts
```

Expected: PASS.

- [ ] **Step 3: Run generated artifact check through API regeneration**

Run:

```bash
make api-generate
git diff --check
```

Expected: `make api-generate` completes. `git diff --check` prints no whitespace errors.

- [ ] **Step 4: Inspect final diff**

Run:

```bash
git diff -- internal/db/types.go internal/db/queries.go internal/db/queries_test.go internal/server/api_types.go internal/server/api_test.go frontend/src/lib/components/terminal/workspaceListSort.ts frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts frontend/tests/e2e-full/00-workspace-sidebar.spec.ts frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts
```

Expected: diff contains only the item activity timestamp exposure, generated API changes, and the new sort option.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/db/types.go internal/db/queries.go internal/db/queries_test.go internal/server/api_types.go internal/server/api_test.go frontend/src/lib/components/terminal/workspaceListSort.ts frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts frontend/tests/e2e-full/00-workspace-sidebar.spec.ts frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts
git commit -m "feat: sort workspaces by item activity" -m "Expose the synced PR/issue last activity timestamp on workspace responses so the workspace rail can sort by provider item changes separately from terminal output activity."
```

Commit body:

```text
Expose the synced PR/issue last activity timestamp on workspace responses
so the workspace rail can sort by provider item changes separately from
terminal output activity.
```

## Self-Review

- Spec coverage: The plan adds one additional sort option in the screenshot menu, sorts by PR/issue `last_activity_at`, and falls back to workspace `created_at` when the underlying item is not synced.
- Placeholder scan: No placeholder steps remain; every code-changing step includes exact fields, snippets, commands, and expected outcomes.
- Type consistency: The timestamp is named `ItemLastActivityAt` in Go and `item_last_activity_at` over JSON/TypeScript, matching existing snake_case API conventions.
