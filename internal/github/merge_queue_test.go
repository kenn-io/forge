package github

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/activityrelay"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

// queuedPR7 is pull request 7 at position 1 of its base branch's queue.
const queuedPR7 = `{"data":{"repository":{"pullRequest":{
	"state":"OPEN","baseRefName":"main","isMergeQueueEnabled":true,
	"mergeQueueEntry":{"position":1,"state":"AWAITING_CHECKS","enqueuedAt":"2026-10-14T19:05:15Z","estimatedTimeToMerge":240},
	"latestQueueEvent":{"nodes":[{"__typename":"AddedToMergeQueueEvent"}]}
}}}}`

// removedPR7 is pull request 7 after its queue removed it for a failed check.
const removedPR7 = `{"data":{"repository":{"pullRequest":{
	"state":"OPEN","baseRefName":"main","isMergeQueueEnabled":true,"mergeQueueEntry":null,
	"latestQueueEvent":{"nodes":[{"__typename":"RemovedFromMergeQueueEvent","createdAt":"2026-10-14T19:06:07Z","reason":"branch_protection_failure"}]}
}}}}`

// mergedByQueuePR7 is pull request 7 after the queue merged it, while GitHub
// still reports it open.
const mergedByQueuePR7 = `{"data":{"repository":{"pullRequest":{
	"state":"OPEN","baseRefName":"main","isMergeQueueEnabled":true,"mergeQueueEntry":null,
	"latestQueueEvent":{"nodes":[{"__typename":"RemovedFromMergeQueueEvent","createdAt":"2026-10-14T19:09:58Z","reason":"merged"}]}
}}}}`

// enqueuedUnlistedPR7 is pull request 7 just after it was enqueued, before
// the queue lists it.
const enqueuedUnlistedPR7 = `{"data":{"repository":{"pullRequest":{
	"state":"OPEN","baseRefName":"main","isMergeQueueEnabled":true,"mergeQueueEntry":null,
	"latestQueueEvent":{"nodes":[{"__typename":"AddedToMergeQueueEvent"}]}
}}}}`

// noQueuePR7 is pull request 7 on a base branch without a merge queue.
const noQueuePR7 = `{"data":{"repository":{"pullRequest":{
	"state":"OPEN","baseRefName":"main","isMergeQueueEnabled":false,"mergeQueueEntry":null,
	"latestQueueEvent":{"nodes":[]}
}}}}`

const squashConfiguration = `{"data":{"repository":{"mergeQueue":{"configuration":{"mergeMethod":"SQUASH"}}}}}`

// refusedResponse answers like GitHub does for a GitHub App without the
// Merge queues permission.
const refusedResponse = `{"data":{"repository":{"pullRequest":null}},` +
	`"errors":[{"type":"FORBIDDEN","path":["repository","pullRequest","mergeQueueEntry"],"message":"Resource not accessible by integration"}]}`

// prQueueGraphQL serves pull request queue reads and queue configuration
// reads, and counts each kind.
type prQueueGraphQL struct {
	mu            sync.Mutex
	pullRequest   string
	configuration string
	prReads       atomic.Int32
	configReads   atomic.Int32
}

func newPRQueueGraphQL(pullRequest string) *prQueueGraphQL {
	return &prQueueGraphQL{pullRequest: pullRequest, configuration: squashConfiguration}
}

func (g *prQueueGraphQL) set(pullRequest string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pullRequest = pullRequest
}

func (g *prQueueGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case bytes.Contains(body, []byte("configuration")):
		g.configReads.Add(1)
		_, _ = io.WriteString(w, g.configuration)
	case bytes.Contains(body, []byte("isMergeQueueEnabled")):
		g.prReads.Add(1)
		_, _ = io.WriteString(w, g.pullRequest)
	case bytes.Contains(body, []byte("pullRequests")):
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequests":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
	default:
		_, _ = io.WriteString(w, `{"data":{"repository":{"issues":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
	}
}

func newMergeQueueTestSyncer(
	t *testing.T, gql http.Handler,
) (*Syncer, *db.DB, RepoRef, int64) {
	t.Helper()
	return newMergeQueueTestSyncerWithClient(t, gql, &mockClient{})
}

func newMergeQueueTestSyncerWithClient(
	t *testing.T, gql http.Handler, client *mockClient,
) (*Syncer, *db.DB, RepoRef, int64) {
	t.Helper()
	database := openTestDB(t)
	repoID, err := reposeed.Seed(t.Context(), database, verifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	for _, number := range []int{7, 8} {
		_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
			RepoID: repoID, PlatformID: int64(7000 + number), Number: number,
			Title: "Queued change", Author: "author", State: db.MergeRequestStateOpen,
			HeadBranch: "feature", BaseBranch: "main",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(t, err)
	}

	srv := httptest.NewServer(gql)
	t.Cleanup(srv.Close)
	repo := RepoRef{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
		Key: platform.RepositoryIDKey(testRepoID("acme", "widgets")),
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(srv.URL, srv.Client()), nil,
		),
	})
	return syncer, database, repo, repoID
}

// newSplitCredentialMergeQueueSyncer routes reads through app and writes
// through user, like a GitHub App sync credential with a user merge token.
func newSplitCredentialMergeQueueSyncer(
	t *testing.T, app, user http.Handler,
) (*Syncer, *db.DB, RepoRef, int64) {
	t.Helper()
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, app)
	appSrv := httptest.NewServer(app)
	t.Cleanup(appSrv.Close)
	userSrv := httptest.NewServer(user)
	t.Cleanup(userSrv.Close)
	router, err := NewHostRouter("github.com", &Route{
		Key:    RouteKey{Host: "github.com", Owner: "acme"},
		Client: &mockClient{},
		Fetcher: NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(appSrv.URL, appSrv.Client()), nil,
		),
		WriteFetcher: NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(userSrv.URL, userSrv.Client()), nil,
		),
		ReadIdentity:  IdentityKey{Host: "github.com", Principal: "app:1"},
		WriteIdentity: IdentityKey{Host: "github.com", Principal: "user:7"},
	})
	require.NoError(t, err)
	syncer.SetGitHubRouters(map[string]*HostRouter{"github.com": router})
	return syncer, database, repo, repoID
}

func relayHintForPR(number int) activityrelay.Hint {
	return activityrelay.Hint{
		Provider: "github", Host: "github.com", RepositoryID: testRepoID("acme", "widgets"),
		Target: activityrelay.PullRequest, Number: number,
	}
}

func TestRefreshPullRequestMergeQueueStoresQueuedState(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := newPRQueueGraphQL(queuedPR7)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	var announced []int
	syncer.SetOnMergeQueueChanged(func(_ context.Context, _ int64, number int) {
		announced = append(announced, number)
	})

	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.True(state.Enabled)
	assert.True(state.Queued)
	assert.Equal(1, state.Position)
	assert.Equal("AWAITING_CHECKS", state.State)
	assert.Equal("squash", state.MergeMethod)
	require.NotNil(state.EstimatedTimeToMerge)
	assert.Equal(4*time.Minute, *state.EstimatedTimeToMerge)
	assert.Equal([]int{7}, announced)

	assert.False(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7),
		"an unchanged queue state is not announced again")
}

func TestRefreshPullRequestMergeQueueKeepsRemovalReason(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := newPRQueueGraphQL(queuedPR7)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))

	gql.set(removedPR7)
	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.False(state.Queued)
	assert.Equal("branch_protection_failure", state.RemovalReason)
	require.NotNil(state.RemovedAt)
	assert.Equal(time.Date(2026, time.October, 14, 19, 6, 7, 0, time.UTC), *state.RemovedAt)

	// Queuing it again replaces the failure.
	gql.set(queuedPR7)
	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	state, err = database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.True(state.Queued)
	assert.Empty(state.RemovalReason)
}

func TestRefreshPullRequestMergeQueueIgnoresQueueMerge(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	gql := newPRQueueGraphQL(mergedByQueuePR7)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7)
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	require.Empty(state.RemovalReason, "leaving the queue because it merged is not a failure")
}

func TestRefreshPullRequestMergeQueueShowsEnqueueBeforeQueueListsIt(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := newPRQueueGraphQL(enqueuedUnlistedPR7)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.True(state.Queued)
	assert.Equal(0, state.Position, "the queue has not reported a position yet")
}

func TestRepositoryPassReadsOnlyQueuedPullRequests(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := newPRQueueGraphQL(queuedPR7)
	client := &mockClient{listOpenPRsErr: notModifiedErr()}
	syncer, database, repo, repoID := newMergeQueueTestSyncerWithClient(t, gql, client)
	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	reads := gql.prReads.Load()

	// The queue removes pull request 7 without touching it; an unchanged
	// pull list still lets the pass notice.
	gql.set(removedPR7)
	syncer.RunOnce(t.Context())

	assert.Equal(reads+1, gql.prReads.Load(), "only the queued pull request is read, not pull request 8")
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.Equal("branch_protection_failure", state.RemovalReason)
}

func TestRelayHintRefreshesPullRequestMergeQueue(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := newPRQueueGraphQL(queuedPR7)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))

	// GitHub sends a pull_request dequeued webhook; the relay forwards it.
	gql.set(removedPR7)
	_ = syncer.refreshRelayHint(WithSyncBudget(t.Context()), relayHintForPR(7))

	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.Equal("branch_protection_failure", state.RemovalReason,
		"the relay hint surfaces the removal without waiting for a pass")
}

func TestRelayHintSkipsPullRequestRecentlySeenWithoutQueue(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := newPRQueueGraphQL(noQueuePR7)
	syncer, _, repo, repoID := newMergeQueueTestSyncer(t, gql)
	now := time.Now().UTC()
	syncer.now = func() time.Time { return now }
	syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7)
	require.EqualValues(1, gql.prReads.Load())

	_ = syncer.refreshRelayHint(WithSyncBudget(t.Context()), relayHintForPR(7))
	assert.EqualValues(1, gql.prReads.Load(), "a base branch without a queue is not re-read on every hint")

	now = now.Add(mergeQueueRecheck + time.Minute)
	_ = syncer.refreshRelayHint(WithSyncBudget(t.Context()), relayHintForPR(7))
	assert.EqualValues(2, gql.prReads.Load(), "it is checked again once the recheck interval passes")
}

func TestManualRefreshSkipsArchivedRepository(t *testing.T) {
	t.Parallel()

	gql := newPRQueueGraphQL(queuedPR7)
	syncer, _, repo, repoID := newMergeQueueTestSyncer(t, gql)
	archived := repo
	archived.Archived = true
	syncer.SetRepos([]RepoRef{archived})

	assert.False(t, syncer.RefreshPullRequestMergeQueueForRepository(t.Context(), repoID, 7))
	assert.Zero(t, gql.prReads.Load(), "an archived repository gets no live queue read")
}

func TestRecordPullRequestEnqueuedShowsQueuedWhenReadFails(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	gql := newPRQueueGraphQL(`{"errors":[{"message":"Something went wrong while executing your query."}]}`)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)

	syncer.RecordPullRequestEnqueued(t.Context(), repo, repoID, 7, "main")
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.True(state.Queued, "Forge shows its own enqueue until a read replaces it")
	assert.Equal(0, state.Position)

	// The next pass re-reads queued pull requests and takes GitHub's state.
	gql.set(queuedPR7)
	syncer.refreshQueuedPullRequests(t.Context(), repo, repoID)
	state, err = database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.Equal(1, state.Position)
}

func TestMergeQueueReadsStopForHostWithoutMergeQueueSchema(t *testing.T) {
	t.Parallel()

	gql := newPRQueueGraphQL(`{"errors":[{"message":"Field 'mergeQueueEntry' doesn't exist on type 'PullRequest'"}]}`)
	syncer, _, repo, repoID := newMergeQueueTestSyncer(t, gql)

	assert.False(t, syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	assert.False(t, syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	assert.EqualValues(t, 1, gql.prReads.Load())
}

// refusingApp answers queue reads like a GitHub App without the Merge queues
// permission, and counts them.
type refusingApp struct {
	reads atomic.Int32
}

func (a *refusingApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	if bytes.Contains(body, []byte("isMergeQueueEnabled")) {
		a.reads.Add(1)
	}
	_, _ = io.WriteString(w, refusedResponse)
}

func TestMergeQueueReadsFallBackToMergeCredentialWhenAppIsRefused(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	user := newPRQueueGraphQL(queuedPR7)
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, &refusingApp{}, user)

	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state, "the merge credential reads the queue the App cannot")
	require.True(state.Queued)
}

func TestMergeQueueReadsStayOnAppWhenItHasPermission(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	app := newPRQueueGraphQL(queuedPR7)
	user := newPRQueueGraphQL(queuedPR7)
	syncer, _, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)

	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	require.Zero(user.prReads.Load(), "a permitted App must not spend the merge credential's quota")
}

func TestRefusedAppIsRetriedAfterRecheckWindow(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	app := &refusingApp{}
	user := newPRQueueGraphQL(queuedPR7)
	syncer, _, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	now := time.Date(2026, time.October, 15, 3, 0, 0, 0, time.UTC)
	syncer.now = func() time.Time { return now }

	syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7)
	syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7)
	assert.EqualValues(1, app.reads.Load(), "a refused App is not asked again inside the recheck window")

	now = now.Add(mergeQueueReadRecheck + time.Minute)
	syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7)
	assert.EqualValues(2, app.reads.Load(), "the App is tried again once the window passes")
}

func TestAppWithMergeQueuesPermissionReadsQueueWithoutConfiguration(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	// The App can read the pull request's queue state but not the queue's
	// configuration, as GitHub allows with only the Merge queues permission.
	app := newPRQueueGraphQL(queuedPR7)
	app.configuration = `{"data":{"repository":{"mergeQueue":{"configuration":null}}},` +
		`"errors":[{"type":"FORBIDDEN","path":["repository","mergeQueue","configuration"],"message":"Resource not accessible by integration"}]}`
	user := newPRQueueGraphQL(queuedPR7)
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)

	require.True(syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7))
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.Equal("squash", state.MergeMethod, "the merge method comes from the merge credential")
	assert.Zero(user.prReads.Load(), "queue reads stay on the App")
	assert.EqualValues(1, user.configReads.Load())

	// Later reads reuse the stored merge method until it is due again.
	syncer.RefreshPullRequestMergeQueue(t.Context(), repo, repoID, 7)
	assert.EqualValues(1, user.configReads.Load())
	state, err = database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Equal("squash", state.MergeMethod)
}
