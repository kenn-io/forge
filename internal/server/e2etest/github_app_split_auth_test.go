package e2etest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/tokenauth"
)

// TestGitHubAppSplitAuthE2E pins the credential split through the
// full stack: a real HTTP server, SQLite, the production token
// collector reading a [[github_apps]] config entry, and the real
// GitHub client transports against a fake GitHub. Sync reads must
// carry the minted installation token while a user-facing mutation
// (posting a PR comment) must carry the user's PAT so GitHub
// attributes it to the user instead of the app bot.
func TestGitHubAppSplitAuthE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "user-pat-e2e")

	var mu sync.Mutex
	authByCall := map[string]string{}
	record := func(name string, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		// Keep the first credential seen per call; retries must not
		// silently overwrite what the upstream actually received.
		if _, ok := authByCall[name]; !ok {
			authByCall[name] = r.Header.Get("Authorization")
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/kenn-io/middleman",
		func(w http.ResponseWriter, r *http.Request) {
			// The repo settings refresh fetches metadata with the app
			// token and overlays viewer permissions from the PAT; the
			// permissions block is viewer-specific (only the PAT can
			// push), so viewer_can_merge in the DB proves the overlay
			// happened.
			permissions := `"permissions": {"push": false}`
			if r.Header.Get("Authorization") == "Bearer user-pat-e2e" {
				record("write:repo-viewer-overlay", r)
				permissions = `"permissions": {"push": true}`
			} else {
				record("read:repo-metadata", r)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{
				"id": 4242001,
				"name": "middleman",
				"full_name": "kenn-io/middleman",
				"owner": {"login": "kenn-io"},
				"default_branch": "main",
				"html_url": "https://github.com/kenn-io/middleman",
				"clone_url": "https://github.com/kenn-io/middleman.git",
				`+permissions+`
			}`)
		})
	mux.HandleFunc("GET /api/v3/repos/kenn-io/middleman/pulls",
		func(w http.ResponseWriter, r *http.Request) {
			record("read:list-pulls", r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[]`)
		})
	mux.HandleFunc("GET /api/v3/repos/kenn-io/middleman/releases",
		func(w http.ResponseWriter, r *http.Request) {
			record("read:releases", r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[]`)
		})
	mux.HandleFunc("POST /api/v3/repos/kenn-io/middleman/issues/7/comments",
		func(w http.ResponseWriter, r *http.Request) {
			record("write:comment", r)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{
				"id": 99001,
				"body": "from middleman",
				"user": {"login": "mariusvniekerk"},
				"created_at": "2026-06-11T17:00:00Z",
				"html_url": "https://github.com/kenn-io/middleman/pull/7#issuecomment-99001"
			}`)
		})
	mux.HandleFunc("GET /api/v3/rate_limit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"resources":{"core":{"limit":5000,"remaining":4999,"reset":2000000000}}}`)
	})
	// Remaining sync reads (issues, labels, tags, ...) are empty lists.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		record("read:other "+r.Method+" "+r.URL.Path, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[]`)
	})
	fakeGitHub := httptest.NewServer(mux)
	t.Cleanup(fakeGitHub.Close)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "kenn-io"
name = "middleman"

[[github_apps]]
host = "github.com"
app_id = 4242
private_key_path = "app.pem"
installation_id = 11
installation_account = "kenn-io"
`), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)

	// Same SourceSet shape main() builds, with the minter stubbed: the
	// JWT-signing exchange itself is pinned by the cmd/middleman
	// startup e2e; this test pins which credential each server code
	// path resolves.
	sourceSet := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			return "ghs_app_token_e2e", time.Now().Add(time.Hour), nil
		},
	})
	var source tokenauth.Source
	for _, plan := range cfg.ProviderTokenSources() {
		src := sourceSet.Upsert(plan.Descriptor)
		if _, err := src.Token(t.Context()); err != nil {
			if !plan.Required && errors.Is(err, tokenauth.ErrMissingToken) {
				continue
			}
			require.NoError(err)
		}
		if plan.Descriptor.Key == (tokenauth.Key{Platform: "github", Host: "github.com"}) {
			source = src
		}
	}
	require.NotNil(source)

	client, err := ghclient.NewClient(
		source, "github.com", nil, nil,
		ghclient.WithBaseURLForTesting(fakeGitHub.URL),
	)
	require.NoError(err)
	registry, err := ghclient.NewProviderRegistry(
		map[string]ghclient.Client{"github.com": client},
	)
	require.NoError(err)

	database := dbtest.Open(t)
	ref := ghclient.RepoRef{
		Platform:           platform.KindGitHub,
		Owner:              "kenn-io",
		Name:               "middleman",
		RepoPath:           "kenn-io/middleman",
		PlatformHost:       "github.com",
		PlatformRepoID:     4242001,
		PlatformExternalID: "4242001",
		DefaultBranch:      "main",
	}
	repoID, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(platform.RepoRef{
		Platform:           platform.KindGitHub,
		Host:               "github.com",
		Owner:              "kenn-io",
		Name:               "middleman",
		RepoPath:           "kenn-io/middleman",
		PlatformID:         4242001,
		PlatformExternalID: "4242001",
		DefaultBranch:      "main",
	}))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7001,
		Number:         7,
		URL:            "https://github.com/kenn-io/middleman/pull/7",
		Title:          "Split auth test PR",
		Author:         "ada",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{ref}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{TokenSources: sourceSet},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	httpServer := httptest.NewServer(srv)
	t.Cleanup(httpServer.Close)

	// Read half: a full repo sync through the API must reach upstream
	// with the app installation token.
	status, body := postJSON(t, httpServer.Client(), httpServer.URL+"/api/v1/sync", nil)
	require.Equal(http.StatusAccepted, status, body)
	waitForRepoSynced(t, database, "kenn-io", "middleman", nil)

	// Write half: posting a PR comment through the API must reach
	// upstream with the user's PAT.
	commentResp := doServerJSON(
		t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/api/v1/pulls/gh/kenn-io/middleman/7/comments",
		map[string]string{"body": "from middleman"},
	)
	defer commentResp.Body.Close()
	commentBody, err := io.ReadAll(commentResp.Body)
	require.NoError(err)
	require.Equal(http.StatusCreated, commentResp.StatusCode, string(commentBody))

	// The repo settings refresh resolved with the PAT, so the stored
	// merge permission must reflect the user, not the read-only app.
	dbRepo, err := database.GetRepoByOwnerName(t.Context(), "kenn-io", "middleman")
	require.NoError(err)
	assert.True(dbRepo.ViewerCanMerge,
		"viewer_can_merge must come from the PAT-visible permissions, not the app's")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal("Bearer ghs_app_token_e2e", authByCall["read:list-pulls"],
		"sync reads must use the minted installation token")
	assert.Equal("Bearer user-pat-e2e", authByCall["write:comment"],
		"mutations must use the user's PAT for attribution")
	assert.Equal("Bearer user-pat-e2e", authByCall["write:repo-viewer-overlay"],
		"the viewer permission overlay must use the user's PAT")
	assert.Equal("Bearer ghs_app_token_e2e", authByCall["read:repo-metadata"],
		"repository metadata must stay on the app token")
	for name, auth := range authByCall {
		if strings.HasPrefix(name, "write:") {
			continue
		}
		assert.Equal("Bearer ghs_app_token_e2e", auth, "call %s", name)
	}
}
