package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitea"
)

func TestSyncerBudgetWaitDuringMergeQueueMethodRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	gql := newPRQueueGraphQL(queuedPR7)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	t.Cleanup(syncer.Stop)
	server := httptest.NewServer(gql)
	t.Cleanup(server.Close)
	budget := NewSyncBudget(1)
	syncer.SetFetchers(map[string]*GraphQLFetcher{"github.com": NewGraphQLFetcherWithClient(
		githubv4.NewEnterpriseClient(server.URL, &http.Client{
			Transport: WrapSyncBudgetTransport(server.Client().Transport, budget),
		}), nil,
	)})
	logs := captureDefaultLogs(t)

	changed, read := syncer.refreshPullRequestMergeQueue(WithSyncBudget(t.Context()), repo, repoID, 7)

	assert.True(changed)
	assert.True(read)
	assert.EqualValues(1, gql.prReads.Load())
	assert.Zero(gql.configReads.Load(), "the queue read spends the final allowance")
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.True(state.Queued)
	assert.Empty(state.MergeMethod)
	assert.NotContains(logs.String(), "level=WARN")
}

func TestSyncerBudgetWaitDuringProviderTagRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repo := RepoRef{Platform: platform.KindGitea, PlatformHost: "gitea.example.com", Owner: "owner", Name: "repo"}
	repoID, err := reposeed.Seed(t.Context(), database, verifiedDBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	var releaseCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releaseCalls.Add(1)
		assert.Equal("/api/v1/repos/owner/repo/releases", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(server.Close)
	provider, err := gitea.NewClient(repo.PlatformHost, testTokenSource("token"),
		gitea.WithBaseURL(server.URL, true),
		gitea.WithTransport(WrapSyncBudgetTransport(server.Client().Transport, NewSyncBudget(1))),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, database, nil, []RepoRef{repo}, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	logs := captureDefaultLogs(t)

	syncer.syncProviderRepoOverview(WithSyncBudget(t.Context()), repo, repoID, false)

	assert.EqualValues(1, releaseCalls.Load())
	assert.NotContains(logs.String(), "level=WARN")
}

func TestSyncerBudgetWaitDuringDetailReads(t *testing.T) {
	for _, site := range []string{"timeline", "detail drain"} {
		t.Run(site, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			database := openTestDB(t)
			repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
			repoID, err := reposeed.Seed(ctx, database, verifiedGitHubRepoIdentity("github.com", repo.Owner, repo.Name))
			require.NoError(err)
			pr := buildOpenPR(1, time.Now().UTC())
			normalized, err := NormalizePR(repoID, pr)
			require.NoError(err)
			mrID, err := database.UpsertMergeRequest(ctx, normalized)
			require.NoError(err)
			client := &partialFailureMock{}
			client.timelineEventsErr = platform.ErrSyncBudgetExhausted
			var detailCalls int
			client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
				detailCalls++
				return nil, platform.ErrSyncBudgetExhausted
			}
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil, testBudget(100))
			t.Cleanup(syncer.Stop)
			logs := captureDefaultLogs(t)

			if site == "timeline" {
				require.NoError(syncer.refreshTimeline(ctx, repo, mrID, mergeRequestSnapshotRevision(t, database, repoID, 1), pr, ""))
			} else {
				syncer.drainDetailQueue(WithSyncBudget(ctx), map[string]bool{"github.com": true}, []RepoRef{repo})
				assert.Equal(1, detailCalls)
				stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
				require.NoError(err)
				require.NotNil(stored)
				assert.Nil(stored.DetailFetchedAt, "refused detail stays pending")
			}
			assert.NotContains(logs.String(), "level=WARN")
		})
	}
}
