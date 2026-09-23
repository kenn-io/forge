package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/settingsapi"
	"go.kenn.io/forge/internal/testutil"
)

func listRepoNames(t *testing.T, srv *Server) []string {
	t.Helper()
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var repos []struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &repos))
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		names = append(names, repo.Name)
	}
	return names
}

func TestDeleteConfiguredRepoClearsOrphanedVisibility(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "wid*"
`, &serverfake.MockGH{
		ListReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{{
				NodeID:   new("R_widget"),
				Name:     new("widget"),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}}, nil
		},
	})
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme",
		Name:           "widget",
	})
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_widget",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.Empty(listRepoNames(t, srv))

	// Removing the exact entry orphans the preference: the glob keeps the
	// repository tracked, but glob rows have no visibility controls, so the
	// preference must be released with its owning exact entry.
	rr = testutil.DoJSON(t, srv, http.MethodDelete,
		"/api/v1/repo/github/acme/widget", nil)

	require.Equal(http.StatusNoContent, rr.Code, rr.Body.String())
	event := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(event.Valid, event.Error)

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	assert.Empty(hidden,
		"deleting the last exact entry clears its hidden preference")
	assert.Equal([]string{"widget"}, listRepoNames(t, srv),
		"the glob-tracked repo returns to the interactive catalog")
}

func TestDeleteConfiguredRepoClearsVisibilityDespiteCanceledRequest(t *testing.T) {
	require := require.New(t)
	srv, database, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "wid*"
`, &serverfake.MockGH{})

	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme",
		Name:           "widget",
	})
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_widget",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	// A client can abandon the DELETE request after the config change
	// commits; cleanup must still run rather than orphan the preference.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := srv.settingsapi.DeleteConfiguredRepo(ctx, &settingsapi.RepoConfigInput{
		Provider: "github",
		Owner:    "acme",
		Name:     "widget",
	})
	require.NoError(err)

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	assert.Empty(t, hidden,
		"a canceled request context must not leave the preference orphaned")
}

func TestConfigReloadClearsOrphanedVisibility(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	// The provider lists acme/widget so the glob keeps resolving the
	// repository after the exact entry is removed from the TOML file.
	mock := &serverfake.MockGH{
		ListReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{{
				NodeID:   new("R_widget"),
				Name:     new("widget"),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}}, nil
		},
	}
	srv, database, cfgPath, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "wid*"
`, mock)

	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme",
		Name:           "widget",
	})
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_widget",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.Empty(listRepoNames(t, srv))

	// Editing the TOML file removes the exact entry while the glob keeps the
	// repository tracked. The reload path must release the preference just
	// like the DELETE handler does.
	writeConfigToml(t, cfgPath, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "wid*"
`)
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.True(event.Valid, event.Error)

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	assert.Empty(hidden,
		"reloading without the exact entry clears its hidden preference")
	assert.Equal([]string{"widget"}, listRepoNames(t, srv),
		"the glob-tracked repo returns to the interactive catalog")
}

func TestRepoUIVisibilityMutationSerializesWithOrphanSweep(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _, syncer := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "wid*"
`, &serverfake.MockGH{})

	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme",
		Name:           "widget",
	})
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_widget",
		ConfiguredRepoPath: "acme/widget",
	}})

	// Hold the visibility lock the way a concurrent delete or hot-reload
	// sweep would, and remove the exact entry while the PUT is blocked. The
	// PUT must revalidate membership inside the critical section: writing
	// against the pre-delete membership check would orphan the preference
	// behind the glob.
	srv.repoVisibilityMu.Lock()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		var buf bytes.Buffer
		buf.WriteString(`{"hidden":true}`)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPut,
			"/api/v1/repo/github/acme/widget/ui-visibility", &buf)
		req.Host = "127.0.0.1:8091"
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		result <- rr
	}()
	select {
	case <-result:
		srv.repoVisibilityMu.Unlock()
		require.FailNow("the visibility mutation ignored the sweep lock")
	case <-time.After(100 * time.Millisecond):
	}

	srv.cfgMu.Lock()
	kept := srv.cfg.Repos[:0:0]
	for _, raw := range srv.cfg.Repos {
		if !raw.HasNameGlob() {
			continue
		}
		kept = append(kept, raw)
	}
	srv.cfg.Repos = kept
	srv.cfgMu.Unlock()
	srv.repoVisibilityMu.Unlock()

	rr := <-result
	require.Equal(http.StatusNotFound, rr.Code, rr.Body.String())
	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	assert.Empty(hidden,
		"a PUT losing the race to a delete must not orphan the preference")
}
