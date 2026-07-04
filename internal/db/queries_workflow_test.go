package db

import (
	"testing"

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
}
