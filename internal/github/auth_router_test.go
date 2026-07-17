package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type routeRecordingClient struct {
	Client
	marker      string
	calls       []string
	snapshot    *RateLimitSnapshot
	snapshotErr error
}

func (c *routeRecordingClient) GetRepository(
	_ context.Context, owner, repo string,
) (*gh.Repository, error) {
	c.calls = append(c.calls, "get:"+owner+"/"+repo)
	return &gh.Repository{Name: new(c.marker)}, nil
}

func (c *routeRecordingClient) ListRepositoriesByOwner(
	_ context.Context, owner string,
) ([]*gh.Repository, error) {
	c.calls = append(c.calls, "list-owner:"+owner)
	return []*gh.Repository{{Name: new(c.marker)}}, nil
}

func (c *routeRecordingClient) CreateIssue(
	_ context.Context, owner, repo, _, _ string,
) (*gh.Issue, error) {
	c.calls = append(c.calls, "create-issue:"+owner+"/"+repo)
	return &gh.Issue{Title: new(c.marker)}, nil
}

func (c *routeRecordingClient) ListRepoLabels(
	_ context.Context, owner, repo string,
) ([]*gh.Label, error) {
	c.calls = append(c.calls, "list-labels:"+owner+"/"+repo)
	return []*gh.Label{{Name: new(c.marker)}}, nil
}

func (c *routeRecordingClient) ReplaceIssueLabels(
	_ context.Context, owner, repo string, _ int, _ []string,
) ([]*gh.Label, error) {
	c.calls = append(c.calls, "labels:"+owner+"/"+repo)
	return []*gh.Label{{Name: new(c.marker)}}, nil
}

func (c *routeRecordingClient) AuthenticatedViewerLogin(context.Context) (string, error) {
	c.calls = append(c.calls, "viewer")
	return c.marker, nil
}

func (c *routeRecordingClient) AuthenticatedViewerCacheKey() string {
	return "viewer:" + c.marker
}

func (c *routeRecordingClient) GetNotificationThread(
	_ context.Context, threadID string,
) (NotificationThread, error) {
	c.calls = append(c.calls, "thread:"+threadID)
	return NotificationThread{ID: threadID}, nil
}

func (c *routeRecordingClient) GetRateLimitSnapshot(context.Context) (*RateLimitSnapshot, error) {
	c.calls = append(c.calls, "snapshot")
	if c.snapshotErr != nil {
		return nil, c.snapshotErr
	}
	if c.snapshot != nil {
		return c.snapshot, nil
	}
	return &RateLimitSnapshot{}, nil
}

func (c *routeRecordingClient) bypassNotificationReadRateReserve() bool {
	return c.marker == "fallback"
}

func (c *routeRecordingClient) ListNotifications(
	_ context.Context, _ NotificationListOptions,
) ([]NotificationThread, bool, error) {
	c.calls = append(c.calls, "notifications")
	return nil, false, nil
}

func TestHostRouterSelectsExactOwnerAndFallbackRoutes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fallback := &Route{
		Key:          RouteKey{Host: "github.com"},
		ReadIdentity: IdentityKey{Host: "github.com", Principal: "user:1"},
	}
	owner := &Route{
		Key:          RouteKey{Host: "github.com", Owner: "Acme"},
		ReadIdentity: IdentityKey{Host: "github.com", Principal: "user:2"},
	}
	exact := &Route{
		Key:          RouteKey{Host: "github.com", Owner: "Acme", Name: "Widget"},
		ReadIdentity: IdentityKey{Host: "github.com", Principal: "user:3"},
	}
	router, err := NewHostRouter("github.com", fallback, owner, exact)
	require.NoError(err)

	got, err := router.RouteForRepo("ACME", "WIDGET")
	require.NoError(err)
	assert.Same(exact, got)
	got, err = router.RouteForRepo("acme", "other")
	require.NoError(err)
	assert.Same(owner, got)
	got, err = router.RouteForOwner("unknown")
	require.NoError(err)
	assert.Same(fallback, got)
	got, err = router.Fallback()
	require.NoError(err)
	assert.Same(fallback, got)

	identity, err := router.ReadIdentityForRepo("acme", "widget")
	require.NoError(err)
	assert.Equal("user:3", identity.Principal)
}

func TestHostRouterRejectsConflictingRouteHostWithoutMutation(t *testing.T) {
	route := &Route{Key: RouteKey{Host: "github.com", Owner: "acme"}}

	_, err := NewHostRouter("ghe.example.com", route)

	require.Error(t, err)
	assert.Equal(t, "github.com", route.Key.Host)
}

func TestHostRouterReturnsSafeMissingRouteError(t *testing.T) {
	require := require.New(t)
	router, err := NewHostRouter("ghe.example.com", nil)
	require.NoError(err)

	_, err = router.RouteForOwner("private-org")

	require.Error(err)
	require.ErrorContains(err, "ghe.example.com")
	require.ErrorContains(err, "private-org")
}

func TestRoutedClientDelegatesByRepositoryOwnerAndFallback(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	fallbackClient := &routeRecordingClient{marker: "fallback"}
	ownerClient := &routeRecordingClient{marker: "owner"}
	exactClient := &routeRecordingClient{marker: "exact"}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com"}, Client: fallbackClient},
		&Route{Key: RouteKey{Host: "github.com", Owner: "acme"}, Client: ownerClient},
		&Route{Key: RouteKey{Host: "github.com", Owner: "acme", Name: "widget"}, Client: exactClient},
	)
	require.NoError(err)
	client, err := NewRoutedClient(router)
	require.NoError(err)

	repo, err := client.GetRepository(t.Context(), "ACME", "WIDGET")
	require.NoError(err)
	assert.Equal("exact", repo.GetName())
	repo, err = client.GetRepository(t.Context(), "acme", "other")
	require.NoError(err)
	assert.Equal("owner", repo.GetName())
	repo, err = client.GetRepository(t.Context(), "other", "repo")
	require.NoError(err)
	assert.Equal("fallback", repo.GetName())

	repos, err := client.ListRepositoriesByOwner(t.Context(), "Acme")
	require.NoError(err)
	assert.Equal("owner", repos[0].GetName())
	issue, err := client.CreateIssue(t.Context(), "acme", "other", "title", "body")
	require.NoError(err)
	assert.Equal("owner", issue.GetTitle())
	beforeNotifications := len(fallbackClient.calls)
	_, _, err = client.ListNotifications(t.Context(), NotificationListOptions{})
	require.NoError(err)
	assert.Equal("notifications", fallbackClient.calls[beforeNotifications])
	beforeOwnerNotifications := len(ownerClient.calls)
	_, _, err = client.ListNotifications(t.Context(), NotificationListOptions{
		RepoOwner: "acme", RepoName: "other",
	})
	require.NoError(err)
	assert.Equal("notifications", ownerClient.calls[beforeOwnerNotifications])
	labels, err := client.ReplaceIssueLabels(
		t.Context(), "acme", "other", 1, []string{"bug"},
	)
	require.NoError(err)
	assert.Equal("owner", labels[0].GetName())
	viewer, err := client.AuthenticatedViewerLogin(t.Context())
	require.NoError(err)
	assert.Equal("fallback", viewer)
	assert.Equal("viewer:fallback", client.AuthenticatedViewerCacheKey())
	thread, err := client.GetNotificationThread(t.Context(), "123")
	require.NoError(err)
	assert.Equal("123", thread.ID)
	_, err = client.GetRateLimitSnapshot(t.Context())
	require.NoError(err)
	assert.True(client.bypassNotificationReadRateReserve())
}

func TestRoutedClientWithoutFallbackRejectsOwnerlessAPIs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ownerClient := &routeRecordingClient{marker: "owner"}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com", Owner: "acme"}, Client: ownerClient},
	)
	require.NoError(err)
	client, err := NewRoutedClient(router)
	require.NoError(err)

	repo, err := client.GetRepository(t.Context(), "acme", "widget")
	require.NoError(err)
	assert.Equal("owner", repo.GetName())
	_, _, err = client.ListNotifications(t.Context(), NotificationListOptions{})
	require.Error(err)
	require.ErrorContains(err, "ownerless request")
}

func TestSyncerFetcherForSelectsRepositoryRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	fallbackFetcher := &GraphQLFetcher{}
	ownerFetcher := &GraphQLFetcher{}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com"}, Fetcher: fallbackFetcher},
		&Route{Key: RouteKey{Host: "github.com", Owner: "acme"}, Fetcher: ownerFetcher},
	)
	require.NoError(err)
	syncer := &Syncer{
		routers: map[string]*HostRouter{"github.com": router},
	}

	assert.Same(ownerFetcher, syncer.fetcherFor(RepoRef{
		Owner: "ACME", Name: "widget", PlatformHost: "github.com",
	}))
	assert.Same(fallbackFetcher, syncer.fetcherFor(RepoRef{
		Owner: "other", Name: "widget", PlatformHost: "github.com",
	}))
}

func TestSyncerSelectsIdentityScopedTrackersAndBudgetsForRepo(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	user123 := IdentityKey{Host: "github.com", Principal: "user:123"}
	user456 := IdentityKey{Host: "github.com", Principal: "user:456"}
	installation := IdentityKey{Host: "github.com", Principal: "installation:789"}
	gql123 := NewRateTracker(database, "github.com", "user:123", "graphql")
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-a"}, Fetcher: &GraphQLFetcher{rateTracker: gql123}, ReadIdentity: user123, WriteIdentity: user123},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-b"}, Fetcher: &GraphQLFetcher{rateTracker: gql123}, ReadIdentity: user123, WriteIdentity: user123},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-c"}, ReadIdentity: user456, WriteIdentity: user456},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-d"}, ReadIdentity: installation, WriteIdentity: user123},
	)
	require.NoError(err)
	rest123 := NewRateTracker(database, "github.com", "user:123", "rest")
	rest456 := NewRateTracker(database, "github.com", "user:456", "rest")
	appREST := NewRateTracker(database, "github.com", "installation:789", "rest")
	budget123 := NewSyncBudget(100)
	budget456 := NewSyncBudget(100)
	appBudget := NewSyncBudget(100)
	syncer := &Syncer{
		routers: map[string]*HostRouter{"github.com": router},
		rateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "user:123"):         rest123,
			RateBucketKey("github", "github.com", "user:456"):         rest456,
			RateBucketKey("github", "github.com", "installation:789"): appREST,
		},
		writeRateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "user:123"): rest123,
		},
		writeGQLRateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "user:123"): gql123,
		},
		budgets: map[string]*SyncBudget{
			RateBucketKey("github", "github.com", "user:123"):         budget123,
			RateBucketKey("github", "github.com", "user:456"):         budget456,
			RateBucketKey("github", "github.com", "installation:789"): appBudget,
		},
	}
	repoA := RepoRef{Owner: "org-a", Name: "one", PlatformHost: "github.com"}
	repoB := RepoRef{Owner: "org-b", Name: "two", PlatformHost: "github.com"}
	repoC := RepoRef{Owner: "org-c", Name: "three", PlatformHost: "github.com"}
	repoD := RepoRef{Owner: "org-d", Name: "four", PlatformHost: "github.com"}

	gotA, ok := syncer.RateTrackerForRepo(repoA, "rest")
	require.True(ok)
	gotB, ok := syncer.RateTrackerForRepo(repoB, "rest")
	require.True(ok)
	gotC, ok := syncer.RateTrackerForRepo(repoC, "rest")
	require.True(ok)
	gotD, ok := syncer.RateTrackerForRepo(repoD, "rest")
	require.True(ok)
	assert.Same(rest123, gotA)
	assert.Same(rest123, gotB)
	assert.Same(rest456, gotC)
	assert.Same(appREST, gotD)
	writeD, ok := syncer.WriteRateTrackerForRepo(repoD, "rest")
	require.True(ok)
	assert.Same(rest123, writeD)
	writeGQLD, ok := syncer.WriteRateTrackerForRepo(repoD, "graphql")
	require.True(ok)
	assert.Same(gql123, writeGQLD)
	budgetA, ok := syncer.BudgetForRepo(repoA)
	require.True(ok)
	budgetB, ok := syncer.BudgetForRepo(repoB)
	require.True(ok)
	budgetC, ok := syncer.BudgetForRepo(repoC)
	require.True(ok)
	budgetD, ok := syncer.BudgetForRepo(repoD)
	require.True(ok)
	assert.Same(budget123, budgetA)
	assert.Same(budget123, budgetB)
	assert.Same(budget456, budgetC)
	assert.Same(appBudget, budgetD)
	gqlTrackers := syncer.GQLRateTrackers()
	assert.Same(gql123, gqlTrackers[gql123.BucketKey()])
}

func TestSyncerDoesNotFallBackToReadTrackerWithoutWriteIdentity(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	appIdentity := IdentityKey{Host: "github.com", Principal: "installation:789"}
	router, err := NewHostRouter(
		"github.com",
		&Route{
			Key:          RouteKey{Host: "github.com", Owner: "org-app"},
			ReadIdentity: appIdentity,
		},
	)
	require.NoError(err)
	appREST := NewRateTracker(database, "github.com", "installation:789", "rest")
	syncer := &Syncer{
		routers: map[string]*HostRouter{"github.com": router},
		rateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "installation:789"): appREST,
		},
	}
	repo := RepoRef{Owner: "org-app", Name: "one", PlatformHost: "github.com"}

	_, ok := syncer.WriteRateTrackerForRepo(repo, "rest")
	require.False(ok)
	_, ok = syncer.WriteIdentityForRepo(repo)
	require.False(ok)
}

func TestSyncerNotificationAdmissionRejectsMissingWriteIdentity(t *testing.T) {
	require := require.New(t)
	appIdentity := IdentityKey{Host: "github.com", Principal: "installation:789"}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-app"}, ReadIdentity: appIdentity},
	)
	require.NoError(err)
	syncer := &Syncer{routers: map[string]*HostRouter{"github.com": router}}

	err = syncer.ensureNotificationPageBudget(
		RepoRef{Owner: "org-app", Name: "one", PlatformHost: "github.com"},
		&routeRecordingClient{},
	)
	require.Error(err)
	require.ErrorContains(err, "no startup-resolved write identity")
}

func TestSyncerNotificationAdmissionUsesRepositoryWriteIdentity(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	user123 := IdentityKey{Host: "github.com", Principal: "user:123"}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com"}, Client: &routeRecordingClient{marker: "fallback"}},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-a"}, Client: &routeRecordingClient{marker: "owner"}, ReadIdentity: IdentityKey{Host: "github.com", Principal: "installation:789"}, WriteIdentity: user123},
	)
	require.NoError(err)
	writeRT := NewRateTracker(database, "github.com", "user:123", "rest")
	writeBudget := NewSyncBudget(1)
	writeBudget.Spend(1)
	syncer := &Syncer{
		routers: map[string]*HostRouter{"github.com": router},
		writeRateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "user:123"): writeRT,
		},
		budgets: map[string]*SyncBudget{
			RateBucketKey("github", "github.com", "user:123"): writeBudget,
		},
	}

	err = syncer.ensureNotificationPageBudget(
		RepoRef{Owner: "org-a", Name: "one", PlatformHost: "github.com"},
		&routeRecordingClient{},
	)
	require.Error(err)
	require.ErrorContains(err, "sync budget exhausted")

	writeBudget.Refund(1)
	writeRT.UpdateFromRate(Rate{
		Limit: 5000, Remaining: 0, Reset: time.Now().Add(time.Hour),
	})
	err = syncer.ensureNotificationPageBudget(
		RepoRef{Owner: "org-a", Name: "one", PlatformHost: "github.com"},
		&routeRecordingClient{marker: "fallback"},
	)
	require.Error(err)
	require.ErrorContains(err, "rate reserve exhausted")
}

func TestSyncerRefreshesRateSnapshotsPerIdentityRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	user123 := IdentityKey{Host: "github.com", Principal: "user:123"}
	user456 := IdentityKey{Host: "github.com", Principal: "user:456"}
	rest123 := NewRateTracker(database, "github.com", "user:123", "rest")
	gql123 := NewRateTracker(database, "github.com", "user:123", "graphql")
	rest456 := NewRateTracker(database, "github.com", "user:456", "rest")
	gql456 := NewRateTracker(database, "github.com", "user:456", "graphql")
	client123 := &routeRecordingClient{snapshot: &RateLimitSnapshot{
		Core:    &Rate{Limit: 5000, Remaining: 4100},
		GraphQL: &Rate{Limit: 5000, Remaining: 4200},
	}}
	client456 := &routeRecordingClient{snapshot: &RateLimitSnapshot{
		Core:    &Rate{Limit: 5000, Remaining: 3100},
		GraphQL: &Rate{Limit: 5000, Remaining: 3200},
	}}
	router, err := NewHostRouter(
		"github.com",
		&Route{
			Key:    RouteKey{Host: "github.com", Owner: "org-a"},
			Client: client123, Fetcher: &GraphQLFetcher{rateTracker: gql123},
			ReadIdentity: user123,
		},
		&Route{
			Key:    RouteKey{Host: "github.com", Owner: "org-c"},
			Client: client456, Fetcher: &GraphQLFetcher{rateTracker: gql456},
			ReadIdentity: user456,
		},
	)
	require.NoError(err)
	syncer := &Syncer{
		clients: registryFromGitHubClients(map[string]Client{"github.com": client123}),
		routers: map[string]*HostRouter{"github.com": router},
		rateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "user:123"): rest123,
			RateBucketKey("github", "github.com", "user:456"): rest456,
		},
		rateLimitSnapshotRefresh: make(map[string]time.Time),
	}

	syncer.RefreshRateLimitSnapshots(t.Context())

	assert.Equal(4100, rest123.Remaining())
	assert.Equal(4200, gql123.Remaining())
	assert.Equal(3100, rest456.Remaining())
	assert.Equal(3200, gql456.Remaining())
	assert.Equal([]string{"snapshot"}, client123.calls)
	assert.Equal([]string{"snapshot"}, client456.calls)
}

func TestSyncerRateSnapshotTriesHealthyRouteForSharedIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	identity := IdentityKey{Host: "github.com", Principal: "user:123"}
	rest := NewRateTracker(database, "github.com", "user:123", "rest")
	failed := &routeRecordingClient{snapshotErr: fmt.Errorf("expired token")}
	healthy := &routeRecordingClient{snapshot: &RateLimitSnapshot{
		Core: &Rate{Limit: 5000, Remaining: 4200},
	}}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com"}, Client: failed, ReadIdentity: identity},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-b"}, Client: healthy, ReadIdentity: identity},
	)
	require.NoError(err)
	syncer := &Syncer{
		clients: registryFromGitHubClients(map[string]Client{"github.com": healthy}),
		routers: map[string]*HostRouter{"github.com": router},
		rateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "user:123"): rest,
		},
		rateLimitSnapshotRefresh: make(map[string]time.Time),
	}

	syncer.RefreshRateLimitSnapshots(t.Context())

	assert.Equal(4200, rest.Remaining())
	assert.Equal([]string{"snapshot"}, failed.calls)
	assert.Equal([]string{"snapshot"}, healthy.calls)
}

func TestSyncerRefreshesWriteOnlyIdentityRateSnapshot(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	appIdentity := IdentityKey{Host: "github.com", Principal: "installation:789"}
	userIdentity := IdentityKey{Host: "github.com", Principal: "user:123"}
	appREST := NewRateTracker(database, "github.com", "installation:789", "rest")
	userREST := NewRateTracker(database, "github.com", "user:123", "rest")
	userGQL := NewRateTracker(database, "github.com", "user:123", "graphql")
	appClient := &routeRecordingClient{snapshot: &RateLimitSnapshot{
		Core: &Rate{Limit: 5000, Remaining: 4100},
	}}
	userClient := &routeRecordingClient{snapshot: &RateLimitSnapshot{
		Core:    &Rate{Limit: 5000, Remaining: 3100},
		GraphQL: &Rate{Limit: 5000, Remaining: 3200},
	}}
	router, err := NewHostRouter(
		"github.com",
		&Route{
			Key:    RouteKey{Host: "github.com", Owner: "org-d"},
			Client: appClient, ReadIdentity: appIdentity,
			WriteSnapshotClient: userClient, WriteIdentity: userIdentity,
		},
	)
	require.NoError(err)
	syncer := &Syncer{
		clients: registryFromGitHubClients(map[string]Client{"github.com": appClient}),
		routers: map[string]*HostRouter{"github.com": router},
		rateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "installation:789"): appREST,
		},
		writeRateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "user:123"): userREST,
		},
		writeGQLRateTrackers: map[string]*RateTracker{
			RateBucketKey("github", "github.com", "user:123"): userGQL,
		},
		rateLimitSnapshotRefresh: make(map[string]time.Time),
	}

	syncer.RefreshRateLimitSnapshots(t.Context())

	assert.Equal(4100, appREST.Remaining())
	assert.Equal(3100, userREST.Remaining())
	assert.Equal(3200, userGQL.Remaining())
	assert.Equal([]string{"snapshot"}, appClient.calls)
	assert.Equal([]string{"snapshot"}, userClient.calls)
}

func TestSyncerSharedIdentityResetResetsEveryRouteBudget(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	identity := IdentityKey{Host: "github.com", Principal: "user:123"}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-a"}, ReadIdentity: identity},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-b"}, ReadIdentity: identity},
	)
	require.NoError(err)
	bucket := RateBucketKey("github", "github.com", "user:123")
	rt := NewRateTracker(database, "github.com", "user:123", "rest")
	budget := NewSyncBudget(100)
	syncer := NewSyncer(nil, database, nil, nil, time.Minute,
		map[string]*RateTracker{bucket: rt}, map[string]*SyncBudget{bucket: budget})
	syncer.SetGitHubRouters(map[string]*HostRouter{"github.com": router})
	initialReset := time.Now().Add(-time.Minute)
	rt.UpdateFromSnapshot(Rate{Limit: 5000, Remaining: 4900, Reset: initialReset})
	budget.Spend(20)

	rt.UpdateFromSnapshot(Rate{
		Limit: 5000, Remaining: 4900, Reset: initialReset.Add(time.Hour),
	})

	budgetA, ok := syncer.BudgetForRepo(RepoRef{Owner: "org-a", Name: "one", PlatformHost: "github.com"})
	require.True(ok)
	budgetB, ok := syncer.BudgetForRepo(RepoRef{Owner: "org-b", Name: "two", PlatformHost: "github.com"})
	require.True(ok)
	assert.Same(budgetA, budgetB)
	assert.Zero(budgetA.Spent())
}

func TestSyncerAppPauseDoesNotDelayPATIdentityOnSameHost(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	appIdentity := IdentityKey{Host: "github.com", Principal: "installation:789"}
	userIdentity := IdentityKey{Host: "github.com", Principal: "user:456"}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-app"}, ReadIdentity: appIdentity},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-pat"}, ReadIdentity: userIdentity},
	)
	require.NoError(err)
	appBucket := RateBucketKey("github", "github.com", "installation:789")
	userBucket := RateBucketKey("github", "github.com", "user:456")
	appRT := NewRateTracker(database, "github.com", "installation:789", "rest")
	userRT := NewRateTracker(database, "github.com", "user:456", "rest")
	appRT.UpdateFromRate(Rate{Limit: 5000, Remaining: 0, Reset: time.Now().Add(time.Hour)})
	syncer := &Syncer{
		routers:      map[string]*HostRouter{"github.com": router},
		rateTrackers: map[string]*RateTracker{appBucket: appRT, userBucket: userRT},
	}

	appKey, err := syncer.bucketKeyForRepo(RepoRef{Owner: "org-app", Name: "one", PlatformHost: "github.com"}, false)
	require.NoError(err)
	patKey, err := syncer.bucketKeyForRepo(RepoRef{Owner: "org-pat", Name: "two", PlatformHost: "github.com"}, false)
	require.NoError(err)
	eligible := syncer.hostEligibility([]string{appKey, patKey}, map[string]time.Time{})
	assert.False(eligible[appBucket])
	assert.True(eligible[userBucket])
}

func TestSyncerCadenceKeysAreIdentityScoped(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	identity123 := IdentityKey{Host: "github.com", Principal: "user:123"}
	identity456 := IdentityKey{Host: "github.com", Principal: "user:456"}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-a"}, ReadIdentity: identity123},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-b"}, ReadIdentity: identity123},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-c"}, ReadIdentity: identity456},
	)
	require.NoError(err)
	syncer := &Syncer{routers: map[string]*HostRouter{"github.com": router}}

	keyA, err := syncer.bucketKeyForRepo(RepoRef{Owner: "org-a", Name: "one", PlatformHost: "github.com"}, false)
	require.NoError(err)
	keyB, err := syncer.bucketKeyForRepo(RepoRef{Owner: "org-b", Name: "two", PlatformHost: "github.com"}, false)
	require.NoError(err)
	keyC, err := syncer.bucketKeyForRepo(RepoRef{Owner: "org-c", Name: "three", PlatformHost: "github.com"}, false)
	require.NoError(err)
	assert.Equal(keyA, keyB)
	assert.NotEqual(keyA, keyC)

	syncer.nextSyncAfter = map[string]time.Time{keyA: time.Now().Add(time.Minute)}
	syncer.nextWatchSyncAfter = map[string]time.Time{keyC: time.Now().Add(time.Minute)}
	assert.Contains(syncer.nextSyncAfter, keyB)
	assert.NotContains(syncer.nextSyncAfter, keyC)
	assert.Contains(syncer.nextWatchSyncAfter, keyC)
	assert.NotContains(syncer.nextWatchSyncAfter, keyA)
}

func TestRoutedClientKeepsDistinctIdentityGoGitHubCachesIsolated(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	resetAt := time.Now().Add(time.Hour).Unix()
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner := githubOwnerFromPath(r.URL.Path)
		calls[owner]++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		remaining := "4999"
		if owner == "org-a" {
			remaining = "0"
		}
		w.Header().Set("X-RateLimit-Remaining", remaining)
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
		_, _ = w.Write([]byte(`{"id":1,"name":"repo","owner":{"login":"` + owner + `"}}`))
	}))
	defer server.Close()

	clientA, err := NewClient(
		testTokenSource("pat-a"), "github.com",
		NewRateTracker(database, "github.com", "user:1", "rest"), nil,
		WithBaseURLForTesting(server.URL),
	)
	require.NoError(err)
	clientB, err := NewClient(
		testTokenSource("pat-b"), "github.com",
		NewRateTracker(database, "github.com", "user:2", "rest"), nil,
		WithBaseURLForTesting(server.URL),
	)
	require.NoError(err)
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-a"}, Client: clientA},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-b"}, Client: clientB},
	)
	require.NoError(err)
	routed, err := NewRoutedClient(router)
	require.NoError(err)

	_, err = routed.GetRepository(t.Context(), "org-a", "repo")
	require.NoError(err)
	_, err = routed.GetRepository(t.Context(), "org-b", "repo")
	require.NoError(err)
	assert.Equal(1, calls["org-a"])
	assert.Equal(1, calls["org-b"])
}

func TestRoutedClientsForSameIdentityUpdateSharedRateTracker(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	resetAt := time.Now().Add(time.Hour).Unix()
	remaining := map[string]string{"org-a": "4998", "org-b": "4997"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner := githubOwnerFromPath(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", remaining[owner])
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
		_, _ = w.Write([]byte(`{"id":1,"name":"repo","owner":{"login":"` + owner + `"}}`))
	}))
	defer server.Close()

	shared := NewRateTracker(database, "github.com", "user:123", "rest")
	clientA, err := NewClient(
		testTokenSource("pat-a"), "github.com", shared, nil,
		WithBaseURLForTesting(server.URL),
	)
	require.NoError(err)
	clientB, err := NewClient(
		testTokenSource("pat-b"), "github.com", shared, nil,
		WithBaseURLForTesting(server.URL),
	)
	require.NoError(err)
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-a"}, Client: clientA},
		&Route{Key: RouteKey{Host: "github.com", Owner: "org-b"}, Client: clientB},
	)
	require.NoError(err)
	routed, err := NewRoutedClient(router)
	require.NoError(err)

	_, err = routed.GetRepository(t.Context(), "org-a", "repo")
	require.NoError(err)
	_, err = routed.GetRepository(t.Context(), "org-b", "repo")
	require.NoError(err)
	assert.Equal(2, shared.RequestsThisHour())
	assert.Equal(4997, shared.Remaining())
	assert.Equal(5000, shared.RateLimit())
}

func TestHostRouterKeepsAuthorizationRoutesSeparateFromSharedIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	identity := IdentityKey{Host: "github.com", Principal: "user:123"}
	routeA := &Route{
		Key:          RouteKey{Host: "github.com", Owner: "org-a"},
		Client:       &routeRecordingClient{marker: "pat-a"},
		ReadIdentity: identity,
	}
	routeB := &Route{
		Key:          RouteKey{Host: "github.com", Owner: "org-b"},
		Client:       &routeRecordingClient{marker: "pat-b"},
		ReadIdentity: identity,
	}
	router, err := NewHostRouter("github.com", nil, routeA, routeB)
	require.NoError(err)

	gotA, err := router.RouteForOwner("org-a")
	require.NoError(err)
	gotB, err := router.RouteForOwner("org-b")
	require.NoError(err)
	assert.NotSame(gotA.Client, gotB.Client)
	assert.Equal(gotA.ReadIdentity, gotB.ReadIdentity)
}
