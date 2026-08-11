package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func seedVerifiedRepo(
	t *testing.T, database *db.DB, identity db.RepoIdentity,
) {
	t.Helper()
	entry, _, err := database.ReconcileRepositoryObservation(
		t.Context(), identity, time.Now().UTC(),
	)
	require.NoError(t, err)
	require.NotNil(t, entry)
}

func settingsReposFromBody(t *testing.T, body []byte) []ghclient.ConfiguredRepoStatus {
	t.Helper()
	var resp struct {
		Repos []ghclient.ConfiguredRepoStatus `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	return resp.Repos
}

func listRepoNames(t *testing.T, srv *Server) []string {
	t.Helper()
	rr := doJSON(t, srv, http.MethodGet, "/api/v1/repos", nil)
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

func TestHandleUpdateRepoUIVisibilityHidesAndShows(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServerWithConfig(t)

	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-acme-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.Equal([]string{"widget"}, listRepoNames(t, srv))

	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos := settingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.True(repos[0].HiddenFromUI,
		"settings response marks the hidden entry")
	assert.Empty(listRepoNames(t, srv),
		"interactive catalog omits the hidden repo")

	// Settings remains the unfiltered management surface.
	rr = doJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos = settingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.True(repos[0].HiddenFromUI)

	rr = doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": false},
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos = settingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.False(repos[0].HiddenFromUI)
	assert.Equal([]string{"widget"}, listRepoNames(t, srv))
}

func TestHandleUpdateRepoUIVisibilityFollowsRenamedRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServerWithConfig(t)

	// The provider renamed acme/widget to acme-renamed/widget-renamed. The
	// tracked ref carries the current route plus exact-entry provenance, and
	// the catalog row holds the stable provider id.
	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme-renamed",
		Name:           "widget-renamed",
	})
	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme-renamed",
		Name:               "widget-renamed",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_widget",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos := settingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.True(repos[0].HiddenFromUI,
		"configured entry reports hidden through rename provenance")
	assert.Equal("acme-renamed/widget-renamed", repos[0].TrackedRepoPath,
		"settings expose the current provider route for selection cleanup")
	assert.Empty(listRepoNames(t, srv),
		"renamed hidden repo stays out of the catalog")
}

func TestRepoUIVisibilityDoesNotFollowReusedRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServerWithConfig(t)

	// R_old was verified at acme/widget and hidden there.
	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_old",
		Owner:          "acme",
		Name:           "widget",
	})
	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_old",
		ConfiguredRepoPath: "acme/widget",
	}})
	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	// The provider deleted acme/widget and a different repository took over
	// the route. The displaced row keeps its old display route without being
	// the current occupant.
	entry, _, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform:       "github",
			PlatformHost:   "github.com",
			PlatformRepoID: "R_new",
			Owner:          "acme",
			Name:           "widget",
		}, time.Now().UTC().Add(time.Second),
	)
	require.NoError(err)
	require.NotNil(entry)
	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_new",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr = doJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos := settingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.False(repos[0].HiddenFromUI,
		"replacement repo must not inherit hidden state through the reused route")
	assert.Equal([]string{"widget"}, listRepoNames(t, srv),
		"replacement repo stays in the interactive catalog")

	// Hiding the entry now targets the replacement's stable identity.
	rr = doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	hiddenIDs := make([]string, 0, len(hidden))
	for _, repo := range hidden {
		hiddenIDs = append(hiddenIDs, repo.PlatformRepoID)
	}
	assert.Contains(hiddenIDs, "R_new",
		"mutation resolves the replacement by stable provider id")
}

func TestRepoUIVisibilityRejectsStaleTrackedIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServerWithConfig(t)

	// R_old owned acme/widget until a different repository took the route.
	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_old",
		Owner:          "acme",
		Name:           "widget",
	})
	entry, _, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform:       "github",
			PlatformHost:   "github.com",
			PlatformRepoID: "R_new",
			Owner:          "acme",
			Name:           "widget",
		}, time.Now().UTC().Add(time.Second),
	)
	require.NoError(err)
	require.NotNil(entry)

	// The tracked snapshot lags reconciliation and still references R_old.
	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_old",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	assert.Empty(hidden,
		"a displaced row must not receive the visibility mutation")
}

func TestDeleteConfiguredRepoClearsOrphanedVisibility(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServerWithConfigContent(t, `
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
`, &mockGH{})

	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme",
		Name:           "widget",
	})
	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_widget",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.Empty(listRepoNames(t, srv))

	// Removing the exact entry orphans the preference: the glob keeps the
	// repository tracked, but glob rows have no visibility controls, so the
	// preference must be released with its owning exact entry.
	rr = doJSON(t, srv, http.MethodDelete,
		"/api/v1/repo/github/acme/widget", nil,
	)
	require.Equal(http.StatusNoContent, rr.Code, rr.Body.String())

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	assert.Empty(hidden,
		"deleting the last exact entry clears its hidden preference")
	assert.Equal([]string{"widget"}, listRepoNames(t, srv),
		"the glob-tracked repo returns to the interactive catalog")
}

func TestDeleteConfiguredRepoClearsVisibilityDespiteCanceledRequest(t *testing.T) {
	require := require.New(t)
	srv, database, _ := setupTestServerWithConfigContent(t, `
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
`, &mockGH{})

	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme",
		Name:           "widget",
	})
	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_widget",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	// A client can abandon the DELETE request after the config change
	// commits; cleanup must still run rather than orphan the preference.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := srv.deleteConfiguredRepo(ctx, &repoConfigInput{
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
	mock := &mockGH{
		listReposByOwnerFn: func(
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
	srv, database, cfgPath := setupTestServerWithConfigContent(t, `
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

	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme",
		Name:           "widget",
	})
	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_widget",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true},
	)
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
	event := srv.applyConfigChange(t.Context())
	require.True(event.Valid, event.Error)

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	assert.Empty(hidden,
		"reloading without the exact entry clears its hidden preference")
	assert.Equal([]string{"widget"}, listRepoNames(t, srv),
		"the glob-tracked repo returns to the interactive catalog")
}

func TestServerStartupClearsOrphanedVisibility(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// The previous process hid acme/widget behind an exact entry and
	// acme/gadget behind its own exact entry, then the maintainer removed the
	// widget entry from the TOML file while the daemon was stopped.
	database := dbtest.Open(t)
	widget, _, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform:       "github",
			PlatformHost:   "github.com",
			PlatformRepoID: "R_widget",
			Owner:          "acme",
			Name:           "widget",
		}, time.Now().UTC(),
	)
	require.NoError(err)
	require.NotNil(widget)
	gadget, _, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform:       "github",
			PlatformHost:   "github.com",
			PlatformRepoID: "R_gadget",
			Owner:          "acme",
			Name:           "gadget",
		}, time.Now().UTC(),
	)
	require.NoError(err)
	require.NotNil(gadget)
	require.NoError(database.SetRepoHiddenFromUI(
		t.Context(), widget.Repository.ID, true,
	))
	require.NoError(database.SetRepoHiddenFromUI(
		t.Context(), gadget.Repository.ID, true,
	))

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "wid*"

[[repos]]
owner = "acme"
name = "gadget"
`), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)

	// Boot restores tracked refs from provider snapshots before the server
	// is constructed; mirror that order here.
	clients := map[string]ghclient.Client{"github.com": &mockGH{}}
	syncer := ghclient.NewSyncer(
		clients, database, nil, []ghclient.RepoRef{
			{
				Owner:              "acme",
				Name:               "widget",
				PlatformHost:       "github.com",
				PlatformExternalID: "R_widget",
				ConfiguredRepoPath: "acme/wid*",
			},
			{
				Owner:              "acme",
				Name:               "gadget",
				PlatformHost:       "github.com",
				PlatformExternalID: "R_gadget",
				ConfiguredRepoPath: "acme/gadget",
			},
		}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
	require.NotNil(srv)

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	hiddenIDs := make([]string, 0, len(hidden))
	for _, repo := range hidden {
		hiddenIDs = append(hiddenIDs, repo.PlatformRepoID)
	}
	assert.Equal([]string{"R_gadget"}, hiddenIDs,
		"startup clears glob-only hidden state but keeps exact-owned state")
}

func TestStartupVisibilitySweepToleratesNilSyncer(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// Servers can be constructed without a syncer. The startup sweep must
	// then resolve exact entries by their configured route instead of
	// panicking on tracked-repo lookup.
	database := dbtest.Open(t)
	widget, _, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform:       "github",
			PlatformHost:   "github.com",
			PlatformRepoID: "R_widget",
			Owner:          "acme",
			Name:           "widget",
		}, time.Now().UTC(),
	)
	require.NoError(err)
	require.NotNil(widget)
	gadget, _, err := database.ReconcileRepositoryObservation(
		t.Context(), db.RepoIdentity{
			Platform:       "github",
			PlatformHost:   "github.com",
			PlatformRepoID: "R_gadget",
			Owner:          "acme",
			Name:           "gadget",
		}, time.Now().UTC(),
	)
	require.NoError(err)
	require.NotNil(gadget)
	require.NoError(database.SetRepoHiddenFromUI(
		t.Context(), widget.Repository.ID, true,
	))
	require.NoError(database.SetRepoHiddenFromUI(
		t.Context(), gadget.Repository.ID, true,
	))

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte(`
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)

	srv := NewWithConfig(
		database, nil, nil, nil, cfg, cfgPath,
		ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
	require.NotNil(srv)

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	hiddenIDs := make([]string, 0, len(hidden))
	for _, repo := range hidden {
		hiddenIDs = append(hiddenIDs, repo.PlatformRepoID)
	}
	assert.Equal([]string{"R_widget"}, hiddenIDs,
		"the configured route keeps its preference; the unconfigured repo is swept")
}

func TestHandleUpdateRepoUIVisibilityRejectsGlobEntries(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget-*"
`, &mockGH{})

	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget-*/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestHandleUpdateRepoUIVisibilityUnknownRepo(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfig(t)

	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/unrelated/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusNotFound, rr.Code, rr.Body.String())
}

func TestHandleUpdateRepoUIVisibilityRequiresVerifiedRepo(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfig(t)

	// acme/widget is configured and tracked but has no catalog row yet
	// (first sync has not verified it), so there is no stable identity to
	// attach the preference to.
	rr := doJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true},
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
}
