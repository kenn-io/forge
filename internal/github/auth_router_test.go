package github

import (
	"context"
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
	marker string
	calls  []string
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
