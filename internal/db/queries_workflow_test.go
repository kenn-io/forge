package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestItemWorkflowStateCRUD(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	insertTestMR(t, d, repoID, 1, "pr 1", baseTime())

	st, err := d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(err)
	assert.Nil(st)

	require.NoError(d.EnsureItemWorkflowState(ctx, repoID, ItemTypePR, 1))
	st, err = d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(st)
	assert.Equal("new", st.Status)

	prev, err := d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:     repoID,
		ItemType:   ItemTypePR,
		ItemNumber: 1,
		Status:     "reviewing",
		Source:     "mcp",
		Actor:      "agent-a",
		Reason:     "recent force push",
	})
	require.NoError(err)
	assert.Equal("new", prev)

	st, err = d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(st)
	assert.Equal("reviewing", st.Status)
	assert.Equal("mcp", st.UpdatedSource)
	assert.Equal("agent-a", st.UpdatedActor)
	assert.Equal("recent force push", st.UpdatedReason)

	require.NoError(d.EnsureItemWorkflowState(ctx, repoID, ItemTypePR, 1))
	st, err = d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(st)
	assert.Equal("reviewing", st.Status)
}

func TestSetItemWorkflowStateExpectedStatus(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	insertTestMR(t, d, repoID, 1, "pr 1", baseTime())

	prev, err := d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:         repoID,
		ItemType:       ItemTypePR,
		ItemNumber:     1,
		Status:         "reviewing",
		ExpectedStatus: "new",
		Source:         "mcp",
	})
	require.NoError(err)
	assert.Equal("new", prev)

	_, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:         repoID,
		ItemType:       ItemTypePR,
		ItemNumber:     1,
		Status:         "waiting",
		ExpectedStatus: "new",
		Source:         "mcp",
	})
	var conflict *WorkflowStateConflictError
	require.ErrorAs(err, &conflict)
	assert.Equal("new", conflict.Expected)
	assert.Equal("reviewing", conflict.Current)

	st, err := d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(st)
	assert.Equal("reviewing", st.Status)

	prev, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:         repoID,
		ItemType:       ItemTypePR,
		ItemNumber:     1,
		Status:         "waiting",
		ExpectedStatus: "reviewing",
		Source:         "api",
	})
	require.NoError(err)
	assert.Equal("reviewing", prev)
}

func TestItemWorkflowStateIssueType(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")

	prev, err := d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:     repoID,
		ItemType:   ItemTypeIssue,
		ItemNumber: 9,
		Status:     "reviewing",
		Source:     "mcp",
	})
	require.NoError(err)
	assert.Equal("new", prev)

	st, err := d.GetItemWorkflowState(ctx, repoID, ItemTypeIssue, 9)
	require.NoError(err)
	require.NotNil(st)
	assert.Equal("reviewing", st.Status)

	st, err = d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 9)
	require.NoError(err)
	assert.Nil(st)
}

func TestItemWorkflowStateValidation(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	insertTestMR(t, d, repoID, 1, "pr 1", baseTime())

	_, err := d.GetItemWorkflowState(ctx, repoID, "pull_request", 1)
	require.Error(err)
	assert.Contains(err.Error(), "invalid item workflow type")

	err = d.EnsureItemWorkflowState(ctx, repoID, "pull_request", 1)
	require.Error(err)
	assert.Contains(err.Error(), "invalid item workflow type")

	_, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:     repoID,
		ItemType:   ItemTypePR,
		ItemNumber: 1,
		Status:     "triaged",
	})
	require.Error(err)
	assert.Contains(err.Error(), "invalid item workflow status")

	_, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:         repoID,
		ItemType:       ItemTypePR,
		ItemNumber:     1,
		Status:         "reviewing",
		ExpectedStatus: "triaged",
	})
	require.Error(err)
	assert.Contains(err.Error(), "invalid item workflow status")

	st, err := d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(err)
	assert.Nil(st)

	_, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:     repoID,
		ItemType:   ItemTypePR,
		ItemNumber: 1,
		Status:     "waiting",
	})
	require.NoError(err)
	_, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:         repoID,
		ItemType:       ItemTypePR,
		ItemNumber:     1,
		Status:         "triaged",
		ExpectedStatus: "waiting",
	})
	require.Error(err)
	st, err = d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(st)
	assert.Equal("waiting", st.Status)
}

func TestListItemWorkflowStates(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()

	insertTestMR(t, d, repoID, 1, "pr one", base.Add(time.Hour))
	insertTestMR(t, d, repoID, 2, "pr two", base.Add(2*time.Hour))
	id3 := insertTestMR(t, d, repoID, 3, "pr three", base.Add(3*time.Hour))
	_, err := d.rw.ExecContext(ctx,
		`UPDATE middleman_merge_requests SET state = 'closed' WHERE id = ?`,
		id3,
	)
	require.NoError(err)
	insertTestIssue(t, d, repoID, 5, "issue five", base.Add(4*time.Hour))

	_, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:     repoID,
		ItemType:   ItemTypePR,
		ItemNumber: 2,
		Status:     "reviewing",
		Source:     "ui",
	})
	require.NoError(err)

	rows, next, err := d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{})
	require.NoError(err)
	assert.Empty(next)
	require.Len(rows, 3)
	assert.Equal(ItemTypePR, rows[0].ItemType)
	assert.Equal(2, rows[0].Number)
	assert.Equal("pr two", rows[0].Title)
	assert.Equal("reviewing", rows[0].Status)
	assert.True(rows[0].HasRow)
	assert.NotNil(rows[0].UpdatedAt)
	assert.Equal("ui", rows[0].UpdatedSource)
	assert.Equal(ItemTypeIssue, rows[1].ItemType)
	assert.Equal(5, rows[1].Number)
	assert.Equal("issue five", rows[1].Title)
	assert.Equal("new", rows[1].Status)
	assert.False(rows[1].HasRow)
	assert.Nil(rows[1].UpdatedAt)
	assert.Equal(ItemTypePR, rows[2].ItemType)
	assert.Equal(1, rows[2].Number)

	rows, next, err = d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{States: []string{"new"}})
	require.NoError(err)
	assert.Empty(next)
	require.Len(rows, 2)
	assert.Equal([]string{ItemTypeIssue, ItemTypePR}, []string{rows[0].ItemType, rows[1].ItemType})

	rows, _, err = d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{IncludeClosed: true})
	require.NoError(err)
	assert.Len(rows, 4)

	rows, _, err = d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{ItemTypes: []string{ItemTypeIssue}})
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal(ItemTypeIssue, rows[0].ItemType)

	_, _, err = d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{ItemTypes: []string{"pull_request"}})
	require.Error(err)
	assert.Contains(err.Error(), "invalid item workflow type")

	_, _, err = d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{States: []string{"triaged"}})
	require.Error(err)
	assert.Contains(err.Error(), "invalid item workflow status")
}

func TestListItemWorkflowStatesCursorPagination(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()
	for i := 1; i <= 5; i++ {
		insertTestMR(t, d, repoID, i, "pr", base.Add(time.Duration(i)*time.Hour))
	}

	var got []int
	cursor := ""
	pages := 0
	for {
		rows, next, err := d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{
			Limit:  2,
			Cursor: cursor,
		})
		require.NoError(err)
		for _, row := range rows {
			got = append(got, row.Number)
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
	}
	assert.Equal([]int{5, 4, 3, 2, 1}, got)
	assert.Equal(3, pages)

	_, _, err := d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{Cursor: "not-base64!!"})
	require.Error(err)
}

func TestListItemWorkflowStatesRepoFiltersUseCasefoldKeys(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	firstRepo, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "Group/SubGroup",
		Name:         "Project.Special",
		RepoPath:     "Group/SubGroup/Project.Special",
	})
	require.NoError(err)
	secondRepo, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "Other/SubGroup",
		Name:         "Project.Special",
		RepoPath:     "Other/SubGroup/Project.Special",
	})
	require.NoError(err)
	insertTestIssue(t, d, firstRepo, 1, "matched issue", base.Add(time.Hour))
	insertTestIssue(t, d, secondRepo, 2, "other issue", base.Add(2*time.Hour))

	rows, next, err := d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{
		RepoFilters: []RepoFilter{{
			Platform:     "gitlab",
			PlatformHost: "GITLAB.EXAMPLE.COM",
			RepoPath:     "group/subgroup/project.special",
		}},
	})
	require.NoError(err)
	assert.Empty(next)
	require.Len(rows, 1)
	assert.NotEqual(secondRepo, firstRepo)
	assert.Equal("gitlab", rows[0].Platform)
	assert.Equal("gitlab.example.com", rows[0].PlatformHost)
	assert.Equal("Group/SubGroup/Project.Special", rows[0].RepoPath)
}

func TestIssueQueriesExposeWorkflowStatus(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	insertTestIssue(t, d, repoID, 5, "issue five", baseTime())

	_, err := d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID:     repoID,
		ItemType:   ItemTypeIssue,
		ItemNumber: 5,
		Status:     "waiting",
		Source:     "api",
	})
	require.NoError(err)

	iss, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 5)
	require.NoError(err)
	require.NotNil(iss)
	assert.Equal(KanbanStatus("waiting"), iss.WorkflowStatus)

	got, err := d.GetIssue(ctx, "github", "github.com", "owner", "repo", 5)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal(KanbanStatus("waiting"), got.WorkflowStatus)

	list, err := d.ListIssues(ctx, ListIssuesOpts{})
	require.NoError(err)
	require.Len(list, 1)
	assert.Equal(KanbanStatus("waiting"), list[0].WorkflowStatus)

	insertTestIssue(t, d, repoID, 6, "issue six", baseTime())
	iss, err = d.GetIssueByRepoIDAndNumber(ctx, repoID, 6)
	require.NoError(err)
	require.NotNil(iss)
	assert.Equal(KanbanStatus(""), iss.WorkflowStatus)
}
