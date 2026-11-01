package github

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	platformgithub "go.kenn.io/forge/platform/github"
)

func TestListValidatorSavedAfterSyncVouchesAfterRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	var conditional, notModified atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v3/repos/acme/widget/pulls", r.URL.Path)
		if match := r.Header.Get("If-None-Match"); match != "" {
			conditional.Add(1)
			if match == `"open-list"` {
				notModified.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.Header().Set("ETag", `"open-list"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number":1,"state":"open"}]`))
	}))
	t.Cleanup(server.Close)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	newProcess := func() (Client, *Syncer) {
		client, err := NewClient(testTokenSource("token"), "github.com", nil, nil,
			WithBaseURLForTesting(server.URL))
		require.NoError(err)
		return client, NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Hour, nil, nil)
	}

	// The first process reads the list and saves its validator once the
	// repository synced.
	client, syncer := newProcess()
	syncCtx, observed := withListETagObservations(ctx)
	_, err := client.ListOpenPullRequests(syncCtx, "acme", "widget")
	require.NoError(err)
	syncer.saveListETags(ctx, repo, observed)

	// A restarted process seeds it, so its first read is a free 304.
	client, syncer = newProcess()
	syncer.seedListETags(ctx)
	_, err = client.ListOpenPullRequests(ctx, "acme", "widget")
	assert.True(platformgithub.IsNotModified(err), "the first read after a restart should revalidate: %v", err)
	assert.Equal(int64(1), notModified.Load())

	// A validator older than the list TTL is not restored.
	saved, err := d.ListRepoListETags(ctx, "github")
	require.NoError(err)
	require.Len(saved, 1)
	saved[0].CachedAt = time.Now().Add(-etagTTL - time.Minute)
	require.NoError(d.ReplaceRepoListETags(ctx, "github", "github.com", "acme", "widget", saved))
	client, syncer = newProcess()
	syncer.seedListETags(ctx)
	_, err = client.ListOpenPullRequests(ctx, "acme", "widget")
	require.NoError(err)
	assert.Equal(int64(1), conditional.Load(), "an expired validator must not be sent")
}

func TestFailedRepoSyncDoesNotSaveListValidators(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	var failIssues atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/repos/acme/widget":
			_, _ = w.Write([]byte(`{"id":42,"name":"widget","full_name":"acme/widget","owner":{"login":"acme"},"default_branch":"main"}`))
		case "/api/v3/repos/acme/widget/pulls":
			w.Header().Set("ETag", `"pulls"`)
			_, _ = w.Write([]byte(`[]`))
		case "/api/v3/repos/acme/widget/issues":
			if failIssues.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Header().Set("ETag", `"issues"`)
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(testTokenSource("token"), "github.com", nil, nil,
		WithBaseURLForTesting(server.URL))
	require.NoError(err)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Hour, nil, nil)

	failIssues.Store(true)
	require.Error(syncer.syncRepo(ctx, repo))
	saved, err := d.ListRepoListETags(ctx, "github")
	require.NoError(err)
	assert.Empty(saved, "a failed sync must not save validators")

	failIssues.Store(false)
	require.NoError(syncer.syncRepo(ctx, repo))
	saved, err = d.ListRepoListETags(ctx, "github")
	require.NoError(err)
	assert.Len(saved, 2, "a successful sync saves its open pull request and issue validators")
}

func TestOverlappingSyncSavesOnlyTheValidatorItRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	var version atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		etag := `"list-` + strconv.FormatInt(version.Load(), 10) + `"`
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(testTokenSource("token"), "github.com", nil, nil,
		WithBaseURLForTesting(server.URL))
	require.NoError(err)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Hour, nil, nil)

	// Sync A reads version 0. Sync B then reads version 1 into the shared
	// cache, but never commits. When A succeeds it saves only what it read.
	ctxA, observedA := withListETagObservations(ctx)
	_, err = client.ListOpenPullRequests(ctxA, "acme", "widget")
	require.NoError(err)
	version.Store(1)
	ctxB, _ := withListETagObservations(ctx)
	_, err = client.ListOpenPullRequests(ctxB, "acme", "widget")
	require.NoError(err)
	syncer.saveListETags(ctx, repo, observedA)

	saved, err := d.ListRepoListETags(ctx, "github")
	require.NoError(err)
	require.Len(saved, 1)
	assert.Equal(`"list-0"`, saved[0].ETag)
}

func TestNativeStacksKeepThePullListUnconditionalAfterRestart(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	var conditional atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"list"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	require.NoError(d.ReplaceRepoListETags(ctx, "github", "github.com", "acme", "widget", []db.RepoListETag{
		{Resource: "pulls?per_page=100&state=open", ETag: `"list"`, CachedAt: time.Now()},
		{Resource: "issues?direction=desc&per_page=100&sort=updated&state=open", ETag: `"list"`, CachedAt: time.Now()},
	}))
	client, err := NewClient(testTokenSource("token"), "github.com", nil, nil,
		WithBaseURLForTesting(server.URL))
	require.NoError(err)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Hour, nil, nil)
	syncer.SetPreferGitHubNativeStacks(true)
	syncer.seedListETags(ctx)

	// Native stack confirmations must be rebuilt, so the pull list reads in
	// full; the issue list still revalidates.
	_, err = client.ListOpenPullRequests(ctx, "acme", "widget")
	require.NoError(err)
	require.Zero(conditional.Load())
	_, err = client.ListOpenIssues(ctx, "acme", "widget")
	require.True(platformgithub.IsNotModified(err), "the issue list still revalidates: %v", err)
	require.Equal(int64(1), conditional.Load())
}

func TestValidatorSeenOnlyThroughA304IsNotSavedUncommitted(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"list-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"list-1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(testTokenSource("token"), "github.com", nil, nil,
		WithBaseURLForTesting(server.URL))
	require.NoError(err)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Hour, nil, nil)

	// Sync B reads the list into the shared cache but never commits it.
	// Sync A then gets a 304 against B's validator and succeeds: it applied
	// nothing, so it must not save B's validator.
	ctxB, _ := withListETagObservations(ctx)
	_, err = client.ListOpenPullRequests(ctxB, "acme", "widget")
	require.NoError(err)
	ctxA, observedA := withListETagObservations(ctx)
	_, err = client.ListOpenPullRequests(ctxA, "acme", "widget")
	require.True(platformgithub.IsNotModified(err))
	syncer.saveListETags(ctx, repo, observedA)

	saved, err := d.ListRepoListETags(ctx, "github")
	require.NoError(err)
	require.Empty(saved)
}

func TestPaginatedListValidatorSurvivesRestartOnlyWhenComplete(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	srv := &largeListServer{}
	server := httptest.NewServer(srv.handler(t))
	t.Cleanup(server.Close)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	newProcess := func() (Client, *Syncer) {
		client, err := NewClient(testTokenSource("token"), "github.com", nil, nil,
			WithBaseURLForTesting(server.URL))
		require.NoError(err)
		return client, NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Hour, nil, nil)
	}
	syncOnce := func(client Client, syncer *Syncer) error {
		syncCtx, observed := withListETagObservations(ctx)
		_, err := client.ListOpenPullRequests(syncCtx, "acme", "widget")
		// Even a sync that succeeds without this list must not save it.
		syncer.saveListETags(ctx, repo, observed)
		return err
	}

	// The first read learns the list is paginated; the second captures and
	// saves the detector.
	client, syncer := newProcess()
	require.NoError(syncOnce(client, syncer))
	require.NoError(syncOnce(client, syncer))

	// After a restart an unchanged list costs one free detector 304.
	client, syncer = newProcess()
	syncer.seedListETags(ctx)
	pages := srv.listPages.Load()
	_, err := client.ListOpenPullRequests(ctx, "acme", "widget")
	require.True(platformgithub.IsNotModified(err), "%v", err)
	assert.Equal(pages, srv.listPages.Load())

	// A change whose later page fails leaves nothing saved to vouch for it.
	srv.generation.Add(1)
	srv.failPage2.Store(true)
	require.Error(syncOnce(client, syncer))
	saved, err := d.ListRepoListETags(ctx, "github")
	require.NoError(err)
	assert.Empty(saved)

	// So after another restart the changed list is read in full.
	srv.failPage2.Store(false)
	client, syncer = newProcess()
	syncer.seedListETags(ctx)
	prs, err := client.ListOpenPullRequests(ctx, "acme", "widget")
	require.NoError(err)
	assert.Len(prs, 2)
}
