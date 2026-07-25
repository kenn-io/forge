package github

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

type stagedReviewSyncProvider struct {
	*syncTestReadProvider
	reviewIDs       []string
	failReviewID    string
	discoveryCalls  int
	hydrationCalls  int
	hydratedReviews []string
	now             time.Time
}

func (p *stagedReviewSyncProvider) ListMergeRequestReviewIDs(
	context.Context,
	platform.RepoRef,
	int,
) ([]string, error) {
	p.discoveryCalls++
	return append([]string(nil), p.reviewIDs...), nil
}

func (p *stagedReviewSyncProvider) ListMergeRequestReviewThreadsForReview(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
	reviewID string,
) ([]platform.MergeRequestReviewThread, error) {
	p.hydrationCalls++
	p.hydratedReviews = append(p.hydratedReviews, reviewID)
	if reviewID == p.failReviewID {
		return nil, errors.New("review hydration failed")
	}
	return []platform.MergeRequestReviewThread{{
		ProviderThreadID:  "thread-" + reviewID,
		ProviderReviewID:  reviewID,
		ProviderCommentID: "comment-" + reviewID,
		Body:              "review body " + reviewID,
		AuthorLogin:       "reviewer",
		DirectURL:         "https://gitea.test/acme/widget/pulls/7#issuecomment-" + reviewID,
		CreatedAt:         p.now,
		UpdatedAt:         p.now,
	}}, nil
}

type stagedReviewSyncFixture struct {
	database *db.DB
	syncer   *Syncer
	provider *stagedReviewSyncProvider
	repo     RepoRef
	repoID   int64
	mrID     int64
}

func newStagedReviewSyncFixture(t *testing.T, reviewCount int) stagedReviewSyncFixture {
	t.Helper()
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitea, PlatformHost: "gitea.test",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
	repoID, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	mrID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 7, PlatformExternalID: "mr-7", Number: 7,
		URL: "https://gitea.test/acme/widget/pulls/7", Title: "cached MR",
		State: db.MergeRequestStateOpen, PlatformHeadSHA: "head-one",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
		LastActivityAt: now.Add(-time.Hour),
	})
	require.NoError(err)
	require.NoError(database.UpsertMRReviewThreads(t.Context(), mrID, []db.MRReviewThread{{
		ProviderThreadID: "cached-thread", Body: "cached complete dataset",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}}))
	reviewIDs := make([]string, reviewCount)
	for i := range reviewIDs {
		reviewIDs[i] = fmt.Sprintf("%d", i+1)
	}
	provider := &stagedReviewSyncProvider{
		syncTestReadProvider: &syncTestReadProvider{
			syncTestProvider: syncTestProvider{kind: platform.KindGitea, host: "gitea.test"},
			mergeRequests: []platform.MergeRequest{{
				Repo: platformRepoRef(repo), PlatformID: 7, PlatformExternalID: "mr-7",
				Number: 7, URL: "https://gitea.test/acme/widget/pulls/7", Title: "fresh MR",
				State: "open", HeadSHA: "head-one", CreatedAt: now.Add(-time.Hour),
				UpdatedAt: now, LastActivityAt: now,
			}},
			readReviewThreads: true,
		},
		reviewIDs: reviewIDs,
		now:       now,
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(
		registry, database, nil, []RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	return stagedReviewSyncFixture{
		database: database, syncer: syncer, provider: provider,
		repo: repo, repoID: repoID, mrID: mrID,
	}
}

func (f stagedReviewSyncFixture) sync(t *testing.T) error {
	t.Helper()
	return f.syncer.SyncMROnProvider(
		t.Context(), f.repo.Platform, f.repo.PlatformHost,
		f.repo.Owner, f.repo.Name, 7,
	)
}

func (f stagedReviewSyncFixture) assertCachedDatasetVisible(t *testing.T) {
	t.Helper()
	assert := assert.New(t)
	require := require.New(t)
	threads, err := f.database.ListMRReviewThreads(t.Context(), f.mrID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("cached-thread", threads[0].ProviderThreadID)
	mr, err := f.database.GetMergeRequestByRepoIDAndNumber(t.Context(), f.repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Nil(mr.DetailFetchedAt)
}

func TestGitealikeReviewHydrationCompletesAcrossCanonicalDetailPasses(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newStagedReviewSyncFixture(t, 17)

	require.NoError(fixture.sync(t))
	fixture.assertCachedDatasetVisible(t)
	stage, err := fixture.database.GetMRReviewHydrationStage(t.Context(), fixture.mrID)
	require.NoError(err)
	require.NotNil(stage)
	assert.Zero(stage.NextReviewIndex)
	assert.Equal(1, fixture.provider.discoveryCalls)
	assert.Zero(fixture.provider.hydrationCalls)

	require.NoError(fixture.sync(t))
	fixture.assertCachedDatasetVisible(t)
	stage, err = fixture.database.GetMRReviewHydrationStage(t.Context(), fixture.mrID)
	require.NoError(err)
	require.NotNil(stage)
	assert.Equal(8, stage.NextReviewIndex)

	require.NoError(fixture.sync(t))
	fixture.assertCachedDatasetVisible(t)
	stage, err = fixture.database.GetMRReviewHydrationStage(t.Context(), fixture.mrID)
	require.NoError(err)
	require.NotNil(stage)
	assert.Equal(16, stage.NextReviewIndex)

	require.NoError(fixture.sync(t))
	stage, err = fixture.database.GetMRReviewHydrationStage(t.Context(), fixture.mrID)
	require.NoError(err)
	assert.Nil(stage)
	assert.Equal(17, fixture.provider.hydrationCalls)
	threads, err := fixture.database.ListMRReviewThreads(t.Context(), fixture.mrID)
	require.NoError(err)
	require.Len(threads, 17)
	assert.Equal("thread-1", threads[0].ProviderThreadID)
	mr, err := fixture.database.GetMergeRequestByRepoIDAndNumber(t.Context(), fixture.repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.NotNil(mr.DetailFetchedAt)
}

func TestArchiveGitealikeReviewHydrationReportsIncompleteUntilFinalSwap(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newStagedReviewSyncFixture(t, 17)
	ref := platformRepoRef(fixture.repo)

	for pass := 1; pass <= 4; pass++ {
		attempted, complete, err := fixture.syncer.SyncArchiveItem(
			WithArchiveAttemptAllowance(WithArchiveSyncBudget(t.Context()), 39),
			ref, db.ArchiveItemTypeMergeRequest, 7,
		)
		require.NoError(err)
		assert.True(attempted)
		assert.Equal(pass == 4, complete)
	}
}

func TestGitealikeReviewHydrationClearsFetchedMarkerWhenStartingStage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newStagedReviewSyncFixture(t, 1)
	mr, err := fixture.database.GetMergeRequestByRepoIDAndNumber(t.Context(), fixture.repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	applied, err := fixture.database.MarkMergeRequestDetailFetchedSnapshot(
		t.Context(), fixture.mrID, mr.SnapshotRevision, false,
	)
	require.NoError(err)
	require.True(applied)

	require.NoError(fixture.sync(t))
	mr, err = fixture.database.GetMergeRequestByRepoIDAndNumber(t.Context(), fixture.repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Nil(mr.DetailFetchedAt)
}

func TestGitealikeReviewHydrationPreservesLiveDatasetOnFailure(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newStagedReviewSyncFixture(t, 9)
	require.NoError(fixture.sync(t))
	fixture.provider.failReviewID = "5"

	require.Error(fixture.sync(t))
	fixture.assertCachedDatasetVisible(t)
	stage, err := fixture.database.GetMRReviewHydrationStage(t.Context(), fixture.mrID)
	require.NoError(err)
	require.NotNil(stage)
	assert.Zero(stage.NextReviewIndex)
	assert.Empty(stage.Threads)

	fixture.provider.failReviewID = ""
	require.NoError(fixture.sync(t))
	fixture.assertCachedDatasetVisible(t)
	require.NoError(fixture.sync(t))
	threads, err := fixture.database.ListMRReviewThreads(t.Context(), fixture.mrID)
	require.NoError(err)
	require.Len(threads, 9)
	assert.Equal("thread-9", threads[8].ProviderThreadID)
}

func TestGitealikeReviewHydrationInvalidatesChangedSnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newStagedReviewSyncFixture(t, 9)
	require.NoError(fixture.sync(t))
	require.NoError(fixture.sync(t))
	stage, err := fixture.database.GetMRReviewHydrationStage(t.Context(), fixture.mrID)
	require.NoError(err)
	require.NotNil(stage)
	assert.Equal(8, stage.NextReviewIndex)
	oldGeneration := stage.Generation

	fixture.provider.reviewIDs = []string{"fresh"}
	fixture.provider.mergeRequests[0].HeadSHA = "head-two"
	fixture.provider.mergeRequests[0].UpdatedAt = fixture.provider.now.Add(time.Minute)
	fixture.provider.mergeRequests[0].LastActivityAt = fixture.provider.now.Add(time.Minute)
	require.NoError(fixture.sync(t))
	fixture.assertCachedDatasetVisible(t)
	stage, err = fixture.database.GetMRReviewHydrationStage(t.Context(), fixture.mrID)
	require.NoError(err)
	require.NotNil(stage)
	assert.Greater(stage.Generation, oldGeneration)
	assert.Equal([]string{"fresh"}, stage.ReviewIDs)
	assert.Zero(stage.NextReviewIndex)
	assert.Empty(stage.Threads)

	require.NoError(fixture.sync(t))
	threads, err := fixture.database.ListMRReviewThreads(t.Context(), fixture.mrID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("thread-fresh", threads[0].ProviderThreadID)
}
