package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotBoundUpdatesRejectAdvancedRevision(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	repoID := insertTestRepo(t, database, "acme", "snapshot-guards")
	mr := &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 1, Title: "old",
		State: MergeRequestStateOpen, PlatformHeadSHA: "head", PlatformBaseSHA: "base",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	}
	mrID, staleRevision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	mr.Title = "current"
	_, currentRevision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	require.Greater(currentRevision, staleRevision)

	applied, err := database.UpdateMergeRequestCISnapshot(ctx, mrID, currentRevision, "success", `[{"name":"current"}]`)
	require.NoError(err)
	require.True(applied)
	applied, err = database.MarkMergeRequestDetailFetchedSnapshot(ctx, mrID, currentRevision, true)
	require.NoError(err)
	require.True(applied)

	applied, err = database.UpdateMergeRequestCISnapshot(ctx, mrID, staleRevision, "failure", `[{"name":"stale"}]`)
	require.NoError(err)
	assert.False(applied)
	applied, err = database.UpdateDiffSHAsSnapshot(
		ctx, mrID, staleRevision, "head", "base", "stale-head", "stale-base", "stale-merge-base",
	)
	require.NoError(err)
	assert.False(applied)
	applied, err = database.ClearMRDetailFetchedSnapshot(ctx, mrID, staleRevision)
	require.NoError(err)
	assert.False(applied)

	current, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(current)
	assert.Equal("success", current.CIStatus)
	assert.NotNil(current.DetailFetchedAt)

	issueID, staleIssueRevision, accepted, err := database.UpsertIssueSnapshotWithLabels(ctx, &Issue{
		RepoID: repoID, PlatformID: 2, Number: 2, Title: "old issue", State: "open",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	_, currentIssueRevision, accepted, err := database.UpsertIssueSnapshotWithLabels(ctx, &Issue{
		RepoID: repoID, PlatformID: 2, Number: 2, Title: "current issue", State: "open",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)
	applied, err = database.MarkIssueDetailFetchedSnapshot(ctx, issueID, currentIssueRevision)
	require.NoError(err)
	require.True(applied)
	applied, err = database.ClearIssueDetailFetchedSnapshot(ctx, issueID, staleIssueRevision)
	require.NoError(err)
	assert.False(applied)
}
