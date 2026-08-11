package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFillMissingMergedMRMetricsFillsOnlyMissingFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID := insertTestRepo(t, database, "acme", "widget")
	mergedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := mergedAt.Add(time.Second + 500*time.Millisecond)

	_, err := database.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 7, Number: 7, Title: "newer title",
		State: MergeRequestStateMerged, PlatformHeadSHA: "head-sha",
		CreatedAt: mergedAt.Add(-time.Hour), UpdatedAt: updatedAt,
		LastActivityAt: updatedAt, MergedAt: &mergedAt,
	})
	require.NoError(err)
	before, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(before)

	changed, err := database.FillMissingMergedMRMetrics(ctx, MergeRequestMergeMetrics{
		RepoID: repoID, Number: 7, HeadSHA: "head-sha",
		MergeCommitSHA: "merge-sha", FilesChanged: 4,
	})
	require.NoError(err)
	assert.True(changed)

	after, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal("merge-sha", after.MergeCommitSHA)
	require.NotNil(after.FilesChanged)
	assert.Equal(4, *after.FilesChanged)
	assert.Equal("newer title", after.Title)
	assert.Equal(before.UpdatedAt, after.UpdatedAt)
	assert.Equal(before.SnapshotRevision, after.SnapshotRevision)
}

func TestFillMissingMergedMRMetricsAcceptsEitherMergedIndicator(t *testing.T) {
	tests := []struct {
		name         string
		state        MergeRequestState
		mergedAtOnly bool
	}{
		{name: "merged state only", state: MergeRequestStateMerged},
		{name: "merged timestamp only", state: MergeRequestStateOpen, mergedAtOnly: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx := t.Context()
			database := openTestDB(t)
			repoID := insertTestRepo(t, database, "acme", "widget")
			mergedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
			var storedMergedAt *time.Time
			if tt.mergedAtOnly {
				storedMergedAt = &mergedAt
			}
			_, err := database.UpsertMergeRequest(ctx, &MergeRequest{
				RepoID: repoID, PlatformID: 7, Number: 7, State: tt.state,
				PlatformHeadSHA: "head-sha", MergedAt: storedMergedAt,
				CreatedAt: mergedAt.Add(-time.Hour), UpdatedAt: mergedAt,
				LastActivityAt: mergedAt,
			})
			require.NoError(err)

			changed, err := database.FillMissingMergedMRMetrics(ctx, MergeRequestMergeMetrics{
				RepoID: repoID, Number: 7, HeadSHA: "head-sha",
				MergeCommitSHA: "merge-sha", FilesChanged: 4, MergedAt: &mergedAt,
			})
			require.NoError(err)
			require.True(changed)

			after, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
			require.NoError(err)
			require.NotNil(after)
			require.Equal("merge-sha", after.MergeCommitSHA)
			require.NotNil(after.FilesChanged)
			require.Equal(4, *after.FilesChanged)
			require.NotNil(after.MergedAt)
			require.Equal(mergedAt, *after.MergedAt)
		})
	}
}

func TestFillMissingMergedMRMetricsPreservesExistingFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	repoID := insertTestRepo(t, database, "acme", "widget")
	mergedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	_, err := database.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 7, Number: 7,
		State: MergeRequestStateMerged, PlatformHeadSHA: "head-sha",
		MergeCommitSHA: "stored-merge-sha",
		CreatedAt:      mergedAt.Add(-time.Hour), UpdatedAt: mergedAt,
		LastActivityAt: mergedAt, MergedAt: &mergedAt,
	})
	require.NoError(err)

	changed, err := database.FillMissingMergedMRMetrics(ctx, MergeRequestMergeMetrics{
		RepoID: repoID, Number: 7, HeadSHA: "head-sha",
		MergeCommitSHA: "provider-merge-sha", FilesChanged: 4,
	})
	require.NoError(err)
	assert.True(changed)

	changed, err = database.FillMissingMergedMRMetrics(ctx, MergeRequestMergeMetrics{
		RepoID: repoID, Number: 7, HeadSHA: "head-sha",
		MergeCommitSHA: "later-merge-sha", FilesChanged: 9,
	})
	require.NoError(err)
	assert.False(changed)

	after, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal("stored-merge-sha", after.MergeCommitSHA)
	require.NotNil(after.FilesChanged)
	assert.Equal(4, *after.FilesChanged)
}

func TestFillMissingMergedMRMetricsRejectsUnprovenIdentity(t *testing.T) {
	tests := []struct {
		name        string
		state       MergeRequestState
		storedHead  string
		inputRepoID func(int64) int64
		inputNumber int
		inputHead   string
		mergeSHA    string
	}{
		{
			name: "open pull request", state: MergeRequestStateOpen,
			storedHead: "head-sha", inputNumber: 7, inputHead: "head-sha",
			mergeSHA: "merge-sha",
		},
		{
			name: "different repository", state: MergeRequestStateMerged,
			storedHead: "head-sha", inputRepoID: func(repoID int64) int64 { return repoID + 1 },
			inputNumber: 7, inputHead: "head-sha", mergeSHA: "merge-sha",
		},
		{
			name: "different number", state: MergeRequestStateMerged,
			storedHead: "head-sha", inputNumber: 8, inputHead: "head-sha",
			mergeSHA: "merge-sha",
		},
		{
			name: "different head", state: MergeRequestStateMerged,
			storedHead: "head-sha", inputNumber: 7, inputHead: "other-head",
			mergeSHA: "merge-sha",
		},
		{
			name: "empty stored head", state: MergeRequestStateMerged,
			inputNumber: 7, inputHead: "head-sha", mergeSHA: "merge-sha",
		},
		{
			name: "empty provider head", state: MergeRequestStateMerged,
			storedHead: "head-sha", inputNumber: 7, mergeSHA: "merge-sha",
		},
		{
			name: "empty provider merge SHA", state: MergeRequestStateMerged,
			storedHead: "head-sha", inputNumber: 7, inputHead: "head-sha",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			database := openTestDB(t)
			repoID := insertTestRepo(t, database, "acme", "widget")
			mergedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
			var storedMergedAt *time.Time
			if tt.state == MergeRequestStateMerged {
				storedMergedAt = &mergedAt
			}
			_, err := database.UpsertMergeRequest(ctx, &MergeRequest{
				RepoID: repoID, PlatformID: 7, Number: 7, State: tt.state,
				PlatformHeadSHA: tt.storedHead, CreatedAt: mergedAt.Add(-time.Hour),
				UpdatedAt: mergedAt, LastActivityAt: mergedAt, MergedAt: storedMergedAt,
			})
			require.NoError(err)

			inputRepoID := repoID
			if tt.inputRepoID != nil {
				inputRepoID = tt.inputRepoID(repoID)
			}
			changed, err := database.FillMissingMergedMRMetrics(ctx, MergeRequestMergeMetrics{
				RepoID: inputRepoID, Number: tt.inputNumber, HeadSHA: tt.inputHead,
				MergeCommitSHA: tt.mergeSHA, FilesChanged: 4,
			})
			require.NoError(err)
			assert.False(changed)

			after, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
			require.NoError(err)
			require.NotNil(after)
			assert.Empty(after.MergeCommitSHA)
			assert.Nil(after.FilesChanged)
		})
	}
}
