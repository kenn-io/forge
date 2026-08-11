package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListWorkspaceSubjectMetadataReturnsPullRequestAndIssue(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widget")
	base := baseTime()
	insertTestMR(t, d, repoID, 4, "live pull request", base)
	insertTestIssue(t, d, repoID, 5, "live issue", base)

	got, err := d.ListWorkspaceSubjectMetadata(t.Context(), []WorkspaceSubjectKey{
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 4},
		{RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 5},
	})
	require.NoError(err)
	require.Len(got, 2)
	assert.Equal("live pull request", got[WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 4,
	}].Title)
	assert.Equal("live issue", got[WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 5,
	}].Title)
}

func TestListWorkspaceSubjectMetadataSupportsLargeSubjectSets(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widget")
	base := baseTime()
	insertTestMR(t, d, repoID, 1, "live pull request", base)

	// Three bind parameters per subject would exceed SQLite's default variable
	// limit here. The request set must remain bounded independently of the
	// number of retained workspaces.
	keys := make([]WorkspaceSubjectKey, 11_000)
	for i := range keys {
		keys[i] = WorkspaceSubjectKey{
			RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: i + 1,
		}
	}

	got, err := d.ListWorkspaceSubjectMetadata(t.Context(), keys)
	require.NoError(err, "metadata lookup failed for %d subjects", len(keys))
	require.Len(got, 1)
	assert.Equal("live pull request", got[WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1,
	}].Title)
}
