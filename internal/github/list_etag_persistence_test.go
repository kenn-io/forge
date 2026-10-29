package github

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	_, err := client.ListOpenPullRequests(ctx, "acme", "widget")
	require.NoError(err)
	syncer.saveListETags(ctx, repo)

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
