package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/tokenauth"
)

// newSplitAuthTestClient builds a liveClient wired exactly like
// NewClient's read/write split (shared auth transport, mutation-marked
// write path) but pointed at srv instead of a real GitHub host.
func newSplitAuthTestClient(
	t *testing.T, srv *httptest.Server, source tokenauth.Source,
) *liveClient {
	t.Helper()
	authRT := tokenauth.AuthTransport{
		Source:              source,
		Base:                http.DefaultTransport,
		SetHeader:           tokenauth.BearerAuthHeader,
		RetryOnUnauthorized: true,
	}
	readHTTP := &http.Client{Transport: authRT}
	writeHTTP := &http.Client{Transport: mutationAuthTransport{base: authRT}}
	ghRead, err := newEnterpriseGHClient(readHTTP,
		srv.URL+"/api/v3/", srv.URL+"/api/uploads/")

	require.NoError(t, err)
	ghWrite, err := newEnterpriseGHClient(writeHTTP,
		srv.URL+"/api/v3/", srv.URL+"/api/uploads/")

	require.NoError(t, err)
	return &liveClient{
		gh:              ghRead,
		ghWrite:         ghWrite,
		source:          source,
		httpClient:      readHTTP,
		httpWriteClient: writeHTTP,
		graphQLEndpoint: srv.URL + "/api/graphql",
	}
}

// TestMutationsUseUserPATWhileReadsUseAppToken pins the credential
// split at the wire level: with a github_app candidate ahead of the
// PAT in the chain, sync reads must authenticate with the minted
// installation token while user-facing writes (REST mutations and the
// ready-for-review GraphQL mutation) must carry the user's PAT so
// GitHub attributes them to the user, not "<app>[bot]".
func TestMutationsUseUserPATWhileReadsUseAppToken(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("TEST_SPLIT_AUTH_PAT", "user-pat")

	var mu sync.Mutex
	authByCall := map[string]string{}
	record := func(name string, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		authByCall[name] = r.Header.Get("Authorization")
	}

	var graphQLCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/acme/widgets/releases",
		func(w http.ResponseWriter, r *http.Request) {
			record("read:releases", r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		})
	mux.HandleFunc("POST /api/v3/repos/acme/widgets/issues/5/comments",
		func(w http.ResponseWriter, r *http.Request) {
			record("write:comment", r)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		})
	mux.HandleFunc("GET /api/v3/repos/acme/widgets",
		func(w http.ResponseWriter, r *http.Request) {
			// Permissions are viewer-specific: only the PAT can push.
			if r.Header.Get("Authorization") == "Bearer user-pat" {
				record("repo:viewer-overlay", r)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":1,"name":"widgets","permissions":{"push":true}}`))
				return
			}
			record("repo:metadata", r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"name":"widgets","permissions":{"push":false}}`))
		})
	mux.HandleFunc("PUT /api/v3/repos/acme/widgets/pulls/5/merge",
		func(w http.ResponseWriter, r *http.Request) {
			record("write:merge", r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"merged":true}`))
		})
	mux.HandleFunc("PUT /api/v3/repos/acme/widgets/pulls/5/reviews/77/dismissals",
		func(w http.ResponseWriter, r *http.Request) {
			record("write:dismiss-review", r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":77,"state":"DISMISSED"}`))
		})
	mux.HandleFunc("POST /api/graphql",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Rate headers only on the node-ID lookup: the lookup
			// consumes the write credential's GraphQL budget too, and
			// the tracker must be fed even when the mutation response
			// carries no rate headers.
			if graphQLCalls.Load() == 0 {
				w.Header().Set("X-RateLimit-Limit", "5000")
				w.Header().Set("X-RateLimit-Remaining", "4321")
				w.Header().Set("X-RateLimit-Reset", "2000000000")
			}
			if graphQLCalls.Add(1) == 1 {
				record("write:rfr-id-lookup", r)
				_, _ = w.Write([]byte(
					`{"data":{"repository":{"pullRequest":{"id":"PR_node"}}}}`,
				))
				return
			}
			record("write:rfr-mutation", r)
			_, _ = w.Write([]byte(
				`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"number":5}}}}`,
			))
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var mints atomic.Int64
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.example.com"},
		Candidates: []tokenauth.Candidate{
			{
				Kind:           tokenauth.SourceKindGitHubApp,
				Host:           "github.example.com",
				FilePath:       "/keys/app.pem",
				AppID:          7,
				InstallationID: 11,
			},
			{Kind: tokenauth.SourceKindEnv, EnvName: "TEST_SPLIT_AUTH_PAT"},
		},
	}, tokenauth.Options{
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			mints.Add(1)
			return "ghs_app_token", time.Now().Add(time.Hour), nil
		},
	})
	c := newSplitAuthTestClient(t, srv, source)
	writeGQLRT := NewRateTracker(openTestDB(t), "github.example.com", "graphql_write")
	c.SetWriteGraphQLRateTracker(writeGQLRT)

	_, err := c.ListReleases(t.Context(), "acme", "widgets", 10)
	require.NoError(err)
	_, err = c.CreateIssueComment(t.Context(), "acme", "widgets", 5, "lgtm")
	require.NoError(err)
	_, err = c.MergePullRequest(t.Context(), "acme", "widgets", 5, "t", "m", "squash", "head-sha")
	require.NoError(err)
	_, err = c.MarkPullRequestReadyForReview(t.Context(), "acme", "widgets", 5)
	require.NoError(err)
	_, err = c.DismissReview(t.Context(), "acme", "widgets", 5, 77, "stale")
	require.NoError(err)
	// GetRepository reads metadata with the sync credential (so
	// app-only hosts keep syncing) and overlays the viewer-specific
	// permissions from the user's credential, which feed
	// viewer_can_merge.
	repo, err := c.GetRepository(t.Context(), "acme", "widgets")
	require.NoError(err)
	assert.True(repo.GetPermissions().GetPush(),
		"permissions must come from the PAT overlay, not the read-only app")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal("Bearer ghs_app_token", authByCall["read:releases"])
	assert.Equal("Bearer user-pat", authByCall["write:comment"])
	assert.Equal("Bearer user-pat", authByCall["write:merge"])
	assert.Equal("Bearer user-pat", authByCall["write:dismiss-review"])
	assert.Equal("Bearer user-pat", authByCall["write:rfr-id-lookup"])
	assert.Equal("Bearer user-pat", authByCall["write:rfr-mutation"])
	assert.Equal("Bearer ghs_app_token", authByCall["repo:metadata"],
		"repository metadata must stay on the sync credential")
	assert.Equal("Bearer user-pat", authByCall["repo:viewer-overlay"])
	assert.Equal(int64(1), mints.Load(),
		"reads share one minted token; writes must not mint")
	// Every write-credential GraphQL request feeds the write GraphQL
	// tracker, including the ready-for-review node-ID lookup — the
	// fake only sets rate headers on the lookup response.
	assert.Equal(4321, writeGQLRT.Remaining())
}

func TestNotificationAPIsUseUserAuthAndBackgroundBudget(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("TEST_NOTIFICATION_AUTH_PAT", "user-pat")

	var mu sync.Mutex
	authByCall := map[string]string{}
	record := func(name string, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		authByCall[name] = r.Header.Get("Authorization")
	}
	setRate := func(w http.ResponseWriter, remaining string) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", remaining)
		w.Header().Set("X-RateLimit-Reset", "2000000000")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/acme/widgets/notifications",
		func(w http.ResponseWriter, r *http.Request) {
			record("notifications:list-repo", r)
			setRate(w, "4990")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{
				"id":"ntf-1",
				"unread":true,
				"reason":"mention",
				"updated_at":"2026-06-17T00:00:00Z",
				"repository":{"name":"widgets","owner":{"login":"acme"}},
				"subject":{"title":"Review","type":"PullRequest","url":"https://github.example.com/api/v3/repos/acme/widgets/pulls/5"}
			}]`))
		})
	mux.HandleFunc("GET /api/v3/notifications/threads/ntf-1",
		func(w http.ResponseWriter, r *http.Request) {
			record("notifications:get", r)
			setRate(w, "4989")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"ntf-1",
				"unread":true,
				"reason":"mention",
				"updated_at":"2026-06-17T00:00:00Z",
				"repository":{"name":"widgets","owner":{"login":"acme"}},
				"subject":{"title":"Review","type":"PullRequest","url":"https://github.example.com/api/v3/repos/acme/widgets/pulls/5"}
			}`))
		})
	mux.HandleFunc("PATCH /api/v3/notifications/threads/ntf-1",
		func(w http.ResponseWriter, r *http.Request) {
			record("notifications:mark-read", r)
			setRate(w, "4988")
			w.WriteHeader(http.StatusNoContent)
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var mints atomic.Int64
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.example.com"},
		Candidates: []tokenauth.Candidate{
			{
				Kind:           tokenauth.SourceKindGitHubApp,
				Host:           "github.example.com",
				FilePath:       "/keys/app.pem",
				AppID:          7,
				InstallationID: 11,
			},
			{Kind: tokenauth.SourceKindEnv, EnvName: "TEST_NOTIFICATION_AUTH_PAT"},
		},
	}, tokenauth.Options{
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			mints.Add(1)
			return "ghs_app_token", time.Now().Add(time.Hour), nil
		},
	})
	database := openTestDB(t)
	readRT := NewRateTracker(database, "github.example.com", "rest")
	writeRT := NewRateTracker(database, "github.example.com", "rest_write")
	budget := NewSyncBudget(100)
	client, err := NewClient(
		source,
		"github.example.com",
		readRT,
		budget,
		WithBaseURLForTesting(srv.URL),
	)
	require.NoError(err)
	c, ok := client.(*liveClient)
	require.True(ok)
	c.SetWriteRateTracker(writeRT)

	syncCtx := WithSyncBudget(t.Context())
	threads, hasNext, err := c.ListNotifications(syncCtx, NotificationListOptions{
		All:       true,
		RepoOwner: "acme",
		RepoName:  "widgets",
	})
	require.NoError(err)
	require.False(hasNext)
	require.Len(threads, 1)
	_, err = c.GetNotificationThread(syncCtx, "ntf-1")
	require.NoError(err)
	require.NoError(c.MarkNotificationThreadRead(syncCtx, "ntf-1"))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal("Bearer user-pat", authByCall["notifications:list-repo"])
	assert.Equal("Bearer user-pat", authByCall["notifications:get"])
	assert.Equal("Bearer user-pat", authByCall["notifications:mark-read"])
	assert.Equal(int64(0), mints.Load(), "notification APIs must not mint app tokens")
	assert.Equal(0, readRT.RequestsThisHour())
	assert.Equal(-1, readRT.Remaining(),
		"PAT notification responses must not overwrite the app-token read tracker")
	assert.Equal(0, writeRT.RequestsThisHour())
	assert.Equal(-1, writeRT.Remaining())
	assert.Equal(3, budget.Spent())
}

// TestMutationAuthFallsBackToReadClientWhenUnsplit pins the hand-built
// client shape used across this package's tests: without a dedicated
// write client, mutations flow through the read client unchanged.
func TestMutationAuthFallsBackToReadClientWhenUnsplit(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/repos/acme/widgets/issues/5/comments",
		func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("TEST_SPLIT_AUTH_PAT", "only-pat")
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.example.com"},
		Candidates: []tokenauth.Candidate{
			{Kind: tokenauth.SourceKindEnv, EnvName: "TEST_SPLIT_AUTH_PAT"},
		},
	}, tokenauth.Options{})
	authRT := tokenauth.AuthTransport{
		Source:    source,
		Base:      http.DefaultTransport,
		SetHeader: tokenauth.BearerAuthHeader,
	}
	ghClient, err := newEnterpriseGHClient(&http.Client{Transport: authRT},
		srv.URL+"/api/v3/", srv.URL+"/api/uploads/")

	require.NoError(t, err)
	c := &liveClient{gh: ghClient}

	_, err = c.CreateIssueComment(t.Context(), "acme", "widgets", 5, "hello")
	require.NoError(t, err)
	assert.Equal(t, "Bearer only-pat", gotAuth)
}
