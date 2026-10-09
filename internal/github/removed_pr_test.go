package github

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

func TestRemovedPRStopsFailingRepositorySync(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(fmt.Sprintf("archived=%t", archived), func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			database := openTestDB(t)
			now := time.Now().UTC()
			repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com", Key: platform.RepositoryIDKey(testRepoID("owner", "repo"))}
			repoID, err := reposeed.Seed(ctx, database, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
			require.NoError(err)
			parent, err := NormalizePR(repoID, buildOpenPR(7, now))
			require.NoError(err)
			_, err = database.UpsertMergeRequest(ctx, parent)
			require.NoError(err)

			if archived {
				require.NoError(database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
				require.NoError(database.StartFullArchives(ctx, []int64{repoID}, now))
				require.NoError(database.CommitArchiveInventoryPage(ctx, db.ArchiveInventoryCommit{
					RepoID: repoID, ItemType: db.ArchiveItemTypeMergeRequest,
					RefreshReason: db.ArchiveRefreshReasonInitial, ScanGeneration: 1,
					Exhausted: true, Now: now,
					Items: []db.ArchiveInventoryItem{{Number: 7, ProviderItemID: "pr-7", ProviderCreatedAt: now, ProviderUpdatedAt: now}},
				}))
			}
			var status atomic.Int32
			status.Store(http.StatusServiceUnavailable)
			var calls atomic.Int32
			client := &partialFailureMock{}
			client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
				calls.Add(1)
				return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: int(status.Load())}, Message: "Not Found"}
			}
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil, nil)
			t.Cleanup(syncer.Stop)
			syncer.RunOnce(ctx)
			storedRepo, err := database.GetRepoByID(ctx, repoID)
			require.NoError(err)
			require.NotNil(storedRepo)
			assert.NotEmpty(storedRepo.LastSyncError)
			status.Store(http.StatusNotFound)
			client.prsCached = false
			syncer.RunOnce(ctx)
			storedRepo, err = database.GetRepoByID(ctx, repoID)
			require.NoError(err)
			assert.Empty(storedRepo.LastSyncError)
			removed, err := database.IsArchiveItemRemovedUpstream(ctx, repoID, db.ArchiveItemTypeMergeRequest, 7)
			require.NoError(err)
			assert.True(removed, "confirmed removal must be recorded")
			storedPR, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
			require.NoError(err)
			require.NotNil(storedPR)
			assert.Equal(db.MergeRequestState("open"), storedPR.State, "retain the last provider snapshot instead of inventing a closure")
			assert.Nil(storedPR.ClosedAt)
			firstCalls := calls.Load()
			require.Positive(firstCalls)
			client.prsCached = false
			syncer.RunOnce(ctx)
			assert.Equal(firstCalls, calls.Load(), "removed PR must not be fetched next cycle")
			require.NoError(syncer.doSyncRepoGraphQL(ctx, repo, repoID, &RepoBulkResult{}, now, false))
			assert.Equal(firstCalls, calls.Load(), "GraphQL closure detection must also skip the removed PR")
		})
	}
}

func TestMissingPRSyncPreservesUncertainLookups(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pullStatus  int
		repoStatus  int
		moved       bool
		routeReused bool
	}{
		{name: "inaccessible", pullStatus: 403},
		{name: "transient pull failure", pullStatus: 503},
		{name: "repository unavailable", pullStatus: 404, repoStatus: 503},
		{name: "repository inaccessible", pullStatus: 404, repoStatus: 404},
		{name: "transferred", moved: true},
		{name: "route reused", pullStatus: 404, routeReused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			database := openTestDB(t)
			now := time.Now().UTC()
			repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com", Key: platform.RepositoryIDKey(testRepoID("owner", "repo"))}
			repoID, err := reposeed.Seed(ctx, database, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
			require.NoError(err)
			parent, err := NormalizePR(repoID, buildOpenPR(7, now))
			require.NoError(err)
			_, err = database.UpsertMergeRequest(ctx, parent)
			require.NoError(err)
			client := &mockClient{}
			client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
				if tc.moved {
					pr := buildOpenPR(7, now)
					pr.Base = &gh.PullRequestBranch{Repo: &gh.Repository{URL: new("https://api.github.com/repos/owner/destination")}}
					return pr, nil
				}
				return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: tc.pullStatus}, Message: "lookup unavailable"}
			}
			if tc.repoStatus != 0 {
				client.getRepositoryFn = func(context.Context, string, string) (*gh.Repository, error) {
					return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: tc.repoStatus}, Message: "repository unavailable"}
				}
			}
			if tc.routeReused {
				client.getRepositoryFn = func(_ context.Context, owner, name string) (*gh.Repository, error) {
					assert.Equal("owner", owner)
					assert.Equal("repo", name)
					return &gh.Repository{ID: new(testRepoID("owner", "replacement")), Owner: &gh.User{Login: &owner}, Name: &name}, nil
				}
			}
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil, nil)
			t.Cleanup(syncer.Stop)
			err = syncer.doSyncRepoGraphQL(ctx, repo, repoID, &RepoBulkResult{}, now, false)
			require.Error(err)
			if tc.routeReused {
				require.ErrorContains(err, "repository identity changed")
				require.NotErrorIs(err, platform.ErrLookupNotPresent)
				require.NotErrorIs(err, platform.ErrProviderContract)
			}
			removed, err := database.IsArchiveItemRemovedUpstream(ctx, repoID, db.ArchiveItemTypeMergeRequest, 7)
			require.NoError(err)
			assert.False(removed)
			pending, err := database.GetPreviouslyOpenMRNumbers(ctx, repoID, nil)
			require.NoError(err)
			assert.Equal([]int{7}, pending)
		})
	}
}

func TestRemovedGraphQLPRsWithoutExternalIDs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	now := time.Now().UTC()
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com", Key: platform.RepositoryIDKey(testRepoID("owner", "repo"))}
	repoID, err := reposeed.Seed(ctx, database, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	for _, number := range []int{7, 8} {
		parent, err := NormalizePR(repoID, platformgithub.AdaptPR(&platformgithub.GraphQLPR{
			DatabaseId: int64(1000 + number), Number: number, State: "OPEN", CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(err)
		require.Empty(parent.PlatformExternalID)
		_, err = database.UpsertMergeRequest(ctx, parent)
		require.NoError(err)
	}
	client := &mockClient{}
	client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
		return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}, Message: "Not Found"}
	}
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	require.NoError(syncer.doSyncRepoGraphQL(ctx, repo, repoID, &RepoBulkResult{}, now, false))
	for _, number := range []int{7, 8} {
		removed, err := database.IsArchiveItemRemovedUpstream(ctx, repoID, db.ArchiveItemTypeMergeRequest, number)
		require.NoError(err)
		assert.True(removed)
		stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
		require.NoError(err)
		require.NotNil(stored)
		assert.Equal(db.MergeRequestState("open"), stored.State)
	}
	pending, err := database.GetPreviouslyOpenMRNumbers(ctx, repoID, nil)
	require.NoError(err)
	assert.Empty(pending)
}
