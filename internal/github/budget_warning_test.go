package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
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

	syncer.refreshPullRequestMergeQueue(WithSyncBudget(t.Context()), repo, repoID, 7)

	assert.EqualValues(1, gql.prReads.Load())
	assert.Zero(gql.configReads.Load(), "the queue read spends the final allowance")
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
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
			var detailCalls int
			if site == "timeline" {
				client.timelineEventsErr = platform.ErrSyncBudgetExhausted
			} else {
				client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
					detailCalls++
					return nil, platform.ErrSyncBudgetExhausted
				}
			}
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil, testBudget(100))
			t.Cleanup(syncer.Stop)
			logs := captureDefaultLogs(t)

			if site == "timeline" {
				require.NoError(syncer.refreshTimeline(ctx, repo, mrID, mergeRequestSnapshotRevision(t, database, repoID, 1), pr.GetNumber(), ""))
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

func TestSyncerBulkBudgetRefusalKeepsStackDetection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	budget := NewSyncBudgetWithEssentialReserve(1000)
	_, spent := budget.TrySpend(budget.BackgroundLimit())
	require.True(spent)
	now := time.Now().UTC().Truncate(time.Second)
	base, child := buildOpenPR(1, now), buildOpenPR(2, now)
	base.Head.Ref, child.Base.Ref = new("feature-a"), new("feature-a")
	child.Head.Ref = new("feature-b")
	var gqlRequests, prOK atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/owner/repo/pulls":
			w.Header().Set("ETag", `"pulls-v1"`)
			if r.Header.Get("If-None-Match") == `"pulls-v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			prOK.Add(1)
			_ = json.NewEncoder(w).Encode([]*gh.PullRequest{base, child})
		case "/api/graphql":
			gqlRequests.Add(1)
		case "/api/v3/repos/owner/repo", "/api/v3/repositories/100":
			_, _ = io.WriteString(w, `{"id":100,"name":"repo","full_name":"owner/repo","owner":{"login":"owner"},"has_issues":true,"default_branch":"main"}`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(testTokenSource("token"), "github.com", nil, budget, WithBaseURLForTesting(server.URL))
	require.NoError(err)
	syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, map[string]*SyncBudget{"github.com": budget})
	t.Cleanup(syncer.Stop)
	syncer.SetFetchers(map[string]*GraphQLFetcher{"github.com": NewGraphQLFetcherWithClient(githubv4.NewEnterpriseClient(
		server.URL+"/api/graphql", &http.Client{Transport: WrapSyncBudgetTransport(server.Client().Transport, budget)},
	), nil)})
	_, err = reposeed.Seed(ctx, database, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", Owner: "owner", Name: "repo",
		Key: platform.RepositoryIDKey(100),
	})
	require.NoError(err)
	var results []RepoSyncResult
	syncer.SetOnSyncCompleted(func(r []RepoSyncResult) { results = r })
	logs := captureDefaultLogs(t)

	syncer.RunOnce(ctx)

	assert.Zero(gqlRequests.Load())
	for _, number := range []int{1, 2} {
		pr, err := database.GetMergeRequest(ctx, "github", "github.com", "owner", "repo", number)
		require.NoError(err)
		require.NotNil(pr)
	}
	assert.Equal(1, strings.Count(logs.String(), `msg="repo waits for sync budget"`))
	assert.Equal(SyncErrorCodeLocalCeilingExhausted, syncer.Status().LastErrorCode)
	require.Len(results, 1)
	require.NotNil(results[0].PartialFailure, "the stack hook skips hard failures")
	assert.False(results[0].PartialFailure.MergeRequests, "REST indexed both PRs, so stack detection must run")

	syncer.RunOnce(ctx)
	assert.Equal(int64(1), prOK.Load(), "the refused bulk read keeps the list ETag")
}

func TestSyncerBudgetWaitDuringCIAndVisibilityReads(t *testing.T) {
	for _, site := range []string{"CI check runs", "PR comment visibility"} {
		t.Run(site, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := WithSyncBudget(t.Context())
			database := openTestDB(t)
			repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
			_, err := reposeed.Seed(ctx, database, verifiedGitHubRepoIdentity("github.com", repo.Owner, repo.Name))
			require.NoError(err)
			budget := NewSyncBudget(1)
			_, spent := budget.TrySpend(1)
			require.True(spent)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(testTokenSource("token"), "github.com", nil, budget, WithBaseURLForTesting(server.URL))
			require.NoError(err)
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil,
				map[string]*SyncBudget{"github.com": budget})
			t.Cleanup(syncer.Stop)
			syncer.SetFetchers(map[string]*GraphQLFetcher{"github.com": NewGraphQLFetcherWithClient(githubv4.NewEnterpriseClient(
				server.URL+"/api/graphql", &http.Client{Transport: WrapSyncBudgetTransport(server.Client().Transport, budget)},
			), nil)})
			logs := captureDefaultLogs(t)

			if site == "CI check runs" {
				result, err := syncer.fetchGitHubCIStatus(ctx, repo, 1, "abc123")
				require.NoError(err)
				assert.False(result.Updated)
			} else {
				_, ok := syncer.currentPRCommentVisibility(ctx, repo, 1)
				assert.False(ok)
			}
			assert.Zero(requests.Load())
			assert.NotContains(logs.String(), "level=WARN")
		})
	}
}
