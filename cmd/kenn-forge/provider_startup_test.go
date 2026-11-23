package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/activityrelay"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

func TestRepoSyncPublishesDataWhileAnotherRepoIsSyncing(t *testing.T) {
	for _, name := range []string{"success", "partial pull request failure", "notification closeout failure"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testRepoSyncPublishesDataWhileAnotherRepoIsSyncing(t, name)
		})
	}
}

func testRepoSyncPublishesDataWhileAnotherRepoIsSyncing(t *testing.T, result string) {
	t.Helper()
	ctx := t.Context()
	database := dbtest.Open(t)
	repoID, err := reposeed.Seed(ctx, database, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "first")), Owner: "acme", Name: "first",
	})
	require.NoError(t, err)
	require.NoError(t, database.UpdateRepoProviderObservation(ctx, repoID, db.RepoProviderMetadata{
		CloneURL: "https://github.com/acme/first.git", DefaultBranch: "main",
	}, nil, nil))
	if result == "partial pull request failure" {
		now := time.Now().UTC()
		_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{RepoID: repoID, PlatformID: 3, Number: 3, Title: "Previous", Author: "user-a", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
		require.NoError(t, err)
	}
	if result == "notification closeout failure" {
		now := time.Now().UTC()
		_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{RepoID: repoID, PlatformID: 3, Number: 3, Title: "Previous", Author: "user-a", State: "closed", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
		require.NoError(t, err)
		require.NoError(t, database.UpsertNotifications(ctx, []db.Notification{{Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-1", RepoID: &repoID, RepoOwner: "acme", RepoName: "first", SubjectType: "PullRequest", SubjectTitle: "Previous", ItemNumber: new(3), ItemType: "pr", Reason: "mention", SourceUpdatedAt: now, SyncedAt: now}}))
		_, err = database.WriteDB().ExecContext(ctx, `CREATE TRIGGER reject_closeout BEFORE UPDATE OF done_at ON forge_notification_items BEGIN SELECT RAISE(ABORT, 'closeout failed'); END`)
		require.NoError(t, err)
	}
	blocked := make(chan struct{})
	release := make(chan struct{})
	var failed atomic.Bool
	client := &serverfake.MockGH{GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
		if result == "partial pull request failure" && number == 3 {
			failed.Store(true)
			return nil, errors.New("pull request unavailable")
		}
		return nil, nil
	}, ListOpenPullRequestsFn: func(ctx context.Context, _, repo string) ([]*gh.PullRequest, error) {
		if repo == "second" {
			close(blocked)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return nil, nil
		}
		now := time.Now().UTC()
		cloneURL := "https://github.com/acme/first.git"
		return []*gh.PullRequest{{
			ID: new(int64(1)), Number: new(1), State: new("open"), Title: new("Example"),
			User: &gh.User{Login: new("user-a")}, CreatedAt: &gh.Timestamp{Time: now}, UpdatedAt: &gh.Timestamp{Time: now},
			Head: &gh.PullRequestBranch{Ref: new("feature"), SHA: new("head"), Repo: &gh.Repository{CloneURL: &cloneURL}},
			Base: &gh.PullRequestBranch{Ref: new("main"), SHA: new("base")},
		}, {
			ID: new(int64(2)), Number: new(2), State: new("open"), Title: new("Example 2"),
			User: &gh.User{Login: new("user-a")}, CreatedAt: &gh.Timestamp{Time: now}, UpdatedAt: &gh.Timestamp{Time: now},
			Head: &gh.PullRequestBranch{Ref: new("feature-2"), SHA: new("head-2"), Repo: &gh.Repository{CloneURL: &cloneURL}},
			Base: &gh.PullRequestBranch{Ref: new("feature"), SHA: new("head")},
		}}, nil
	}}
	repos := []github.RepoRef{{Owner: "acme", Name: "first", PlatformHost: "github.com", CloneURL: "https://github.com/acme/first.git"}, {Owner: "acme", Name: "second", PlatformHost: "github.com"}}
	syncer := github.NewSyncer(map[string]github.Client{"github.com": client}, database, nil, repos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	hub := syncevents.NewEventHub()
	t.Cleanup(hub.Close)
	events, _ := hub.Subscribe(ctx, false)
	syncer.SetOnRepoSynced(repoSyncedHook(ctx, database, hub))
	finished := make(chan struct{})
	go func() { defer close(finished); syncer.RunOnce(ctx) }()
	t.Cleanup(func() { close(release); <-finished })

	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("second repository did not start")
	}
	select {
	case event := <-events:
		require.Equal(t, "data_changed", event.Event.Type)
	case <-time.After(10 * time.Second):
		t.Fatal("no data_changed event while the second repository was syncing")
	}
	prs, err := database.ListMergeRequests(ctx, db.ListMergeRequestsOpts{})
	require.NoError(t, err)
	if result == "partial pull request failure" {
		require.True(t, failed.Load())
		require.Len(t, prs, 3)
		return
	}
	if result == "notification closeout failure" {
		var syncError string
		require.NoError(t, database.ReadDB().QueryRowContext(ctx, `SELECT last_sync_error FROM forge_repos WHERE id = ?`, repoID).Scan(&syncError))
		require.Contains(t, syncError, "closeout failed")
		require.Len(t, prs, 2)
		require.ElementsMatch(t, []int{1, 2}, []int{prs[0].Number, prs[1].Number})
		return
	}
	require.Len(t, prs, 2)
	stack, members, err := database.GetStackForPR(ctx, "github", "github.com", "acme", "first", 1)
	require.NoError(t, err)
	require.NotNil(t, stack)
	require.Len(t, members, 2)
	require.Equal(t, 1, members[0].Number)
	require.Equal(t, 2, members[1].Number)
}

func TestWireSyncStatusDoesNotReloadDataForRelayConnectionChanges(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	syncer := github.NewSyncer(nil, dbtest.Open(t), nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	hub := syncevents.NewEventHub()
	t.Cleanup(hub.Close)
	events, _ := hub.Subscribe(ctx, false)
	wireSyncStatus(syncer, hub)
	feed := new(activityrelay.Broadcaster)
	_, handler := activityrelay.Handlers(feed, nil)
	relay := httptest.NewServer(handler)
	t.Cleanup(relay.Close)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		syncer.RunRelay(ctx, github.RelayOptions{URL: relay.URL, Client: relay.Client()})
	}()
	t.Cleanup(func() { cancel(); <-stopped })
	nextEvent := func() syncevents.RecordedEvent {
		t.Helper()
		select {
		case event := <-events:
			return event
		case <-time.After(10 * time.Second):
			require.FailNow("hub event did not arrive")
			return syncevents.RecordedEvent{}
		}
	}
	initial := nextEvent()
	assert.Equal("sync_status", initial.Event.Type)
	assert.Nil(initial.Event.Data.(*github.SyncStatus).Relay)
	var connected *github.SyncStatus
	for connected == nil {
		event := nextEvent()
		require.Equal("sync_status", event.Event.Type, "relay connection changes must not broadcast data_changed")
		if status := event.Event.Data.(*github.SyncStatus); status.Relay != nil && status.Relay.Connected {
			connected = status
		}
	}
	assert.Empty(connected.Relay.Recent)

	// Ordinary sync completion must still refresh visible data, once.
	syncer.RunOnce(ctx)
	var kinds []string
	for len(kinds) < 3 {
		kinds = append(kinds, nextEvent().Event.Type)
	}
	assert.Equal([]string{"sync_status", "sync_status", "data_changed"}, kinds)
}

func TestBuildServeControlPlanesNodeNeverConstructsProviderPlane(t *testing.T) {
	assert := assert.New(t)
	var factoryCalls atomic.Int32
	var appMints atomic.Int32
	cfg := &config.Config{
		Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleSpoke,
			Hub: &config.FleetHub{
				NodeID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				BaseURL: "https://hub.example",
			},
		},
		Repos: []config.Repo{{Owner: "acme", Name: "widget"}},
		GitHubApps: []config.GitHubAppConfig{{
			Host: "github.com", AppID: 7, PrivateKeyPath: "/keys/app.pem",
			InstallationID: 11, InstallationAccount: "acme",
			RepositorySelection: "all",
		}},
	}
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			appMints.Add(1)
			return "provider-token", time.Now().Add(time.Hour), nil
		},
	})
	planes, err := buildServeControlPlanes(
		t.Context(), nil, cfg, set,
		map[string]providerFactory{
			"github": func(context.Context, providerFactoryInput) (providerFactoryOutput, error) {
				factoryCalls.Add(1)
				return providerFactoryOutput{}, nil
			},
		},
		fakeGitHubIdentityResolver{}, false,
	)
	require.NoError(t, err)
	assert.Nil(planes.Provider)
	assert.Zero(factoryCalls.Load())
	assert.Zero(appMints.Load())
	assert.NotNil(planes.Git.SourceForRepo(
		"github", "github.com", "acme", "widget",
	))
}

func TestDisableSyncRetainsHubProviderPlaneButNeverCreatesNodePlane(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubCLI: func(context.Context, string) (string, error) {
			return "", tokenauth.ErrMissingToken
		},
	})
	hub, err := buildServeControlPlanes(
		t.Context(), dbtest.Open(t), &config.Config{}, set,
		defaultProviderFactories(), nil, true,
	)
	require.NoError(err)
	require.NotNil(hub.Provider)
	assert.NotNil(hub.Provider.registry)
	provider, err := hub.Provider.registry.Provider(
		platform.KindGitHub, platform.DefaultGitHubHost,
	)
	require.NoError(err)
	assert.NotNil(provider)

	spoke, err := buildServeControlPlanes(
		t.Context(), nil, &config.Config{Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleSpoke,
			Hub: &config.FleetHub{
				NodeID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				BaseURL: "https://hub.example",
			},
		}}, set, defaultProviderFactories(), nil, true,
	)
	require.NoError(err)
	assert.Nil(spoke.Provider)
}

func TestGitStartupAllowsAnonymousReadsButRequiredRoutesFailClosed(t *testing.T) {
	require := require.New(t)
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubCLI: func(context.Context, string) (string, error) {
			return "", tokenauth.ErrMissingToken
		},
	})
	optional, err := buildGitStartup(&config.Config{}, set)
	require.NoError(err)

	publicSource := optional.SourceForRepo(
		string(platform.KindGitHub), "github.com", "acme", "public",
	)
	require.NotNil(publicSource)
	token, err := publicSource.Token(t.Context())
	require.NoError(err)
	assert.Empty(t, token, "an optional missing credential must preserve anonymous Git")
	manager := gitclone.New(t.TempDir(), optional)
	require.ErrorIs(manager.RequireCredentialRoute(
		t.Context(), string(platform.KindGitHub), "github.com", "acme", "public",
	), gitclone.ErrCredentialUnavailable)
	readToken, err := optional.ReadSourceForRepo(
		string(platform.KindGitHub), "github.com", "acme", "public",
	).Token(t.Context())
	require.NoError(err)
	assert.Empty(t, readToken, "optional clone reads must preserve anonymous Git")

	requiredRoutes, err := buildGitStartup(&config.Config{
		Repos: []config.Repo{{Owner: "acme", Name: "private"}},
	}, set)
	require.NoError(err)
	requiredSource := requiredRoutes.SourceForRepo(
		string(platform.KindGitHub), "github.com", "acme", "private",
	)
	require.NotNil(requiredSource)
	_, err = requiredSource.Token(t.Context())
	require.ErrorIs(err, tokenauth.ErrMissingToken)
	_, err = requiredRoutes.ReadSourceForRepo(
		string(platform.KindGitHub), "github.com", "acme", "private",
	).Token(t.Context())
	require.ErrorIs(err, tokenauth.ErrMissingToken)
}

func TestDefaultProviderFactoryPassesGitLabSharedSyncBudget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("factory-token", r.Header.Get("Private-Token"))
		assert.Equal("/api/v4/projects/42", r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"path":"project","path_with_namespace":"group/project","name":"Project"}`))
	}))
	defer server.Close()

	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	host := strings.TrimPrefix(server.URL, "https://")
	budget := github.NewSyncBudget(100)
	factory := defaultProviderFactories()[string(platform.KindGitLab)]
	built, err := factory(t.Context(), providerFactoryInput{
		host: host,
		tokenSource: mainTestTokenSource(
			t, string(platform.KindGitLab), host, "GITLAB_FACTORY_TOKEN", "factory-token",
		),
		budget: budget,
	})
	require.NoError(err)
	reader, ok := built.provider.(platform.RepositoryReader)
	require.True(ok)

	_, err = reader.GetRepository(github.WithSyncBudget(t.Context()), platform.RepoRef{
		Platform: platform.KindGitLab, Host: host,
		Owner: "group", Name: "project", RepoPath: "group/project", Key: platform.RepositoryIDKey(42),
	})
	require.NoError(err)
	assert.Equal(1, budget.Spent())
}

func TestDefaultProviderFactoryUsesGiteaExplicitBaseURL(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("token factory-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"1.26.0"}`))
		case "/api/v1/repos/owner/repo":
			_, _ = w.Write([]byte(`{
				"id":42,"name":"repo","full_name":"owner/repo",
				"clone_url":"http://gitea.test/owner/repo.git",
				"owner":{"login":"owner"}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	factory := defaultProviderFactories()[string(platform.KindGitea)]
	built, err := factory(t.Context(), providerFactoryInput{
		host:          "gitea.test",
		baseURL:       server.URL,
		allowInsecure: true,
		tokenSource: mainTestTokenSource(
			t, string(platform.KindGitea), "gitea.test", "GITEA_FACTORY_TOKEN", "factory-token",
		),
	})
	require.NoError(err)
	reader, ok := built.provider.(platform.RepositoryReader)
	require.True(ok)

	repo, err := reader.GetRepository(t.Context(), platform.RepoRef{
		Platform: platform.KindGitea, Host: "gitea.test", Owner: "owner", Name: "repo",
	})
	require.NoError(err)
	assert.Equal("http://gitea.test/owner/repo.git", repo.CloneURL)
}

func TestBitbucketFactorySelectsCloudAndDataCenterAPI(t *testing.T) {
	for _, host := range []string{"bitbucket.org", "bitbucket.example.com"} {
		t.Run(host, func(t *testing.T) {
			assert := assert.New(t)
			original := http.DefaultTransport
			http.DefaultTransport = platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body string
				if host == "bitbucket.org" {
					assert.Equal("api.bitbucket.org", r.URL.Host)
					assert.Equal("/2.0/repositories/PROJECT/widgets", r.URL.Path)
					body = `{"uuid":"{11111111-1111-4111-8111-111111111111}","full_name":"PROJECT/widgets"}`
				} else {
					assert.Equal(host, r.URL.Host)
					if strings.HasSuffix(r.URL.Path, "/default-branch") {
						body = `{"displayId":"main"}`
					} else {
						assert.Equal("/rest/api/latest/projects/PROJECT/repos/widgets", r.URL.Path)
						body = `{"id":42,"slug":"widgets","project":{"key":"PROJECT"}}`
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = original })
			built, err := defaultProviderFactories()["bitbucket"](t.Context(), providerFactoryInput{host: host, tokenSource: mainTestTokenSource(t, "bitbucket", host, "BITBUCKET_FACTORY_TOKEN", "token")})
			require.NoError(t, err)
			reader, ok := built.provider.(platform.RepositoryReader)
			require.True(t, ok)
			repo, err := reader.GetRepository(t.Context(), platform.RepoRef{Platform: platform.KindBitbucket, Host: host, Owner: "PROJECT", Name: "widgets"})
			require.NoError(t, err)
			assert.Equal(host, repo.Ref.Host)
		})
	}
}
