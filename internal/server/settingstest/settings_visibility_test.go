package settingstest

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestHandleUpdateRepoUIVisibilityHidesAndShows(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServerWithConfig(t)

	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-acme-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.Equal([]string{"widget"}, servertest.ListRepoNames(t, srv))

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos := serverfake.SettingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.True(repos[0].HiddenFromUI,
		"settings response marks the hidden entry")
	assert.Empty(servertest.ListRepoNames(t, srv),
		"interactive catalog omits the hidden repo")

	// Settings remains the unfiltered management surface.
	rr = testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos = serverfake.SettingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.True(repos[0].HiddenFromUI)

	rr = testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": false})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos = serverfake.SettingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.False(repos[0].HiddenFromUI)
	assert.Equal([]string{"widget"}, servertest.ListRepoNames(t, srv))
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
	clients := map[string]ghclient.Client{"github.com": &serverfake.MockGH{}}
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
	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
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

func TestHandleUpdateRepoUIVisibilityWithoutSyncer(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// A server without a syncer still owns visibility mutations; persisting
	// the change and then failing to build the settings response would leave
	// the client without the saved state.
	database := dbtest.Open(t)
	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme",
		Name:           "widget",
	})

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
	srv := server.NewWithConfig(
		database, nil, nil, nil, cfg, cfgPath,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos := serverfake.SettingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.True(repos[0].HiddenFromUI,
		"the response reports the saved state without tracked refs")

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	require.Len(hidden, 1)
	assert.Equal("R_widget", hidden[0].PlatformRepoID)
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

	srv := server.NewWithConfig(
		database, nil, nil, nil, cfg, cfgPath,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
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
`, &serverfake.MockGH{})

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget-*/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestHandleUpdateRepoUIVisibilityUnknownRepo(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfig(t)

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/unrelated/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusNotFound, rr.Code, rr.Body.String())
}

func TestHandleUpdateRepoUIVisibilityRequiresVerifiedRepo(t *testing.T) {
	require := require.New(t)
	srv, _, _ := setupTestServerWithConfig(t)

	// acme/widget is configured and tracked but has no catalog row yet
	// (first sync has not verified it), so there is no stable identity to
	// attach the preference to.
	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
}
