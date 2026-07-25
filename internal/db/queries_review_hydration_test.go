package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMRReviewHydrationStageReplacesChangedSnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	repoID := insertTestRepo(t, database, "acme", "widget")
	mrID, _, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, &MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 7, Title: "review stage",
		State: MergeRequestStateOpen, PlatformHeadSHA: "head-one",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.True(accepted)

	stage, err := database.ReplaceMRReviewHydrationStage(ctx, MRReviewHydrationStageKey{
		MergeRequestID: mrID, ProviderUpdatedAt: now, HeadSHA: "head-one",
	}, []string{"99", "100"})
	require.NoError(err)
	assert.Equal(int64(1), stage.Generation)
	assert.Equal([]string{"99", "100"}, stage.ReviewIDs)
	assert.Zero(stage.NextReviewIndex)

	applied, err := database.AppendMRReviewHydrationStage(ctx, *stage, []MRReviewHydrationThread{{
		MRReviewThread: MRReviewThread{
			ProviderThreadID: "thread-one", ProviderReviewID: "99", ProviderCommentID: "101",
			Body: "staged one", CreatedAt: now, UpdatedAt: now,
		},
		DirectURL: "https://gitea.test/acme/widget/pulls/7#issuecomment-101",
	}}, 1)
	require.NoError(err)
	require.True(applied)
	loaded, err := database.GetMRReviewHydrationStage(ctx, mrID)
	require.NoError(err)
	require.NotNil(loaded)
	assert.Equal(1, loaded.NextReviewIndex)
	require.Len(loaded.Threads, 1)
	assert.Equal("thread-one", loaded.Threads[0].ProviderThreadID)
	assert.Contains(loaded.Threads[0].DirectURL, "issuecomment-101")

	replacement, err := database.ReplaceMRReviewHydrationStage(ctx, MRReviewHydrationStageKey{
		MergeRequestID: mrID, ProviderUpdatedAt: now.Add(time.Minute), HeadSHA: "head-two",
	}, []string{"200"})
	require.NoError(err)
	assert.Equal(int64(2), replacement.Generation)
	assert.Zero(replacement.NextReviewIndex)
	assert.Equal([]string{"200"}, replacement.ReviewIDs)
	assert.Empty(replacement.Threads)

	applied, err = database.AppendMRReviewHydrationStage(ctx, *stage, nil, 2)
	require.NoError(err)
	assert.False(applied)
}

func TestMRReviewHydrationStageCommitsAtomicallyAtCurrentRevision(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC)
	repoID := insertTestRepo(t, database, "acme", "atomic-widget")
	mr := &MergeRequest{
		RepoID: repoID, PlatformID: 2, Number: 8, Title: "old parent",
		State: MergeRequestStateOpen, PlatformHeadSHA: "head",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
	}
	mrID, staleRevision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	require.NoError(database.UpsertMRReviewThreads(ctx, mrID, []MRReviewThread{{
		ProviderThreadID: "old-thread", Body: "old complete dataset",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}}))

	stage, err := database.ReplaceMRReviewHydrationStage(ctx, MRReviewHydrationStageKey{
		MergeRequestID: mrID, ProviderUpdatedAt: now, HeadSHA: "head",
	}, []string{"99", "100"})
	require.NoError(err)
	applied, err := database.AppendMRReviewHydrationStage(ctx, *stage, []MRReviewHydrationThread{
		{
			MRReviewThread: MRReviewThread{
				ProviderThreadID: "new-one", ProviderReviewID: "99", ProviderCommentID: "101",
				Body: "new one", AuthorLogin: "ada", CreatedAt: now, UpdatedAt: now,
			},
			DirectURL: "https://gitea.test/acme/atomic-widget/pulls/8#issuecomment-101",
		},
		{
			MRReviewThread: MRReviewThread{
				ProviderThreadID: "new-two", ProviderReviewID: "100", ProviderCommentID: "102",
				Body: "new two", AuthorLogin: "grace", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
			},
			DirectURL: "https://gitea.test/acme/atomic-widget/pulls/8#issuecomment-102",
		},
	}, 2)
	require.NoError(err)
	require.True(applied)

	visible, err := database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(visible, 1)
	assert.Equal("old-thread", visible[0].ProviderThreadID)

	mr.Title = "current parent"
	_, currentRevision, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, mr)
	require.NoError(err)
	require.True(accepted)
	require.Greater(currentRevision, staleRevision)
	ready, err := database.GetMRReviewHydrationStage(ctx, mrID)
	require.NoError(err)
	require.NotNil(ready)

	applied, err = database.CommitMRReviewHydrationStage(ctx, *ready, staleRevision)
	require.NoError(err)
	assert.False(applied)
	visible, err = database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(visible, 1)
	assert.Equal("old-thread", visible[0].ProviderThreadID)
	retained, err := database.GetMRReviewHydrationStage(ctx, mrID)
	require.NoError(err)
	assert.NotNil(retained)

	applied, err = database.CommitMRReviewHydrationStage(ctx, *ready, currentRevision)
	require.NoError(err)
	require.True(applied)
	visible, err = database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(visible, 2)
	assert.Equal("new-one", visible[0].ProviderThreadID)
	assert.Equal("new-two", visible[1].ProviderThreadID)
	finished, err := database.GetMRReviewHydrationStage(ctx, mrID)
	require.NoError(err)
	assert.Nil(finished)
	events, err := database.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 2)
	directURLs := make(map[string]string, len(events))
	for _, event := range events {
		directURLs[event.DedupeKey] = event.DirectURL
	}
	assert.Contains(directURLs["review_comment:101"], "issuecomment-101")
	assert.Contains(directURLs["review_comment:102"], "issuecomment-102")
}
