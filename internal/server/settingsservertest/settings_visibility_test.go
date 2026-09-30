package settingsservertest

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/platform"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil"
)

func TestHandleUpdateRepoUIVisibilityFollowsRenamedRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _, syncer := servertest.SetupTestServerWithConfig(t)

	// The provider renamed acme/widget to acme-renamed/widget-renamed. The
	// tracked ref carries the current route plus exact-entry provenance, and
	// the catalog row holds the stable provider id.
	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Key:          platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")),
		Owner:        "acme-renamed",
		Name:         "widget-renamed",
	})
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme-renamed",
		Name:               "widget-renamed",
		PlatformHost:       "github.com",
		Key:                platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")),
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos := serverfake.SettingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.True(repos[0].HiddenFromUI,
		"configured entry reports hidden through rename provenance")
	assert.Equal("acme-renamed/widget-renamed", repos[0].TrackedRepoPath,
		"settings expose the current provider route for selection cleanup")
	assert.Empty(servertest.ListRepoNames(t, srv),
		"renamed hidden repo stays out of the catalog")
}

func TestRepoUIVisibilityDoesNotFollowReusedRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _, syncer := servertest.SetupTestServerWithConfig(t)

	// R_old was verified at acme/widget and hidden there.
	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Key:          platform.RepositoryIDKey(1002),
		Owner:        "acme",
		Name:         "widget",
	})
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		Key:                platform.RepositoryIDKey(1002),
		ConfiguredRepoPath: "acme/widget",
	}})
	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	// The provider deleted acme/widget and a different repository took over
	// the route. The displaced row keeps its old display route without being
	// the current occupant.
	entry, err := database.ObserveRepository(t.Context(), db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Key:          platform.RepositoryIDKey(1003),
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	require.NotNil(entry)
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		Key:                platform.RepositoryIDKey(1003),
		ConfiguredRepoPath: "acme/widget",
	}})

	rr = testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos := serverfake.SettingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.False(repos[0].HiddenFromUI,
		"replacement repo must not inherit hidden state through the reused route")
	assert.Equal([]string{"widget"}, servertest.ListRepoNames(t, srv),
		"replacement repo stays in the interactive catalog")

	// Hiding the entry now targets the replacement's stable identity.
	rr = testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	hiddenKeys := make([]platform.RepositoryKey, 0, len(hidden))
	for _, repo := range hidden {
		hiddenKeys = append(hiddenKeys, repo.Key)
	}
	assert.Contains(hiddenKeys, platform.RepositoryIDKey(1003),
		"mutation resolves the replacement by stable provider id")
}

func TestRepoUIVisibilityRejectsStaleTrackedIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _, syncer := servertest.SetupTestServerWithConfig(t)

	// R_old owned acme/widget until a different repository took the route.
	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Key:          platform.RepositoryIDKey(1002),
		Owner:        "acme",
		Name:         "widget",
	})
	entry, err := database.ObserveRepository(t.Context(), db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Key:          platform.RepositoryIDKey(1003),
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	require.NotNil(entry)

	// The tracked snapshot lags reconciliation and still references R_old.
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		Key:                platform.RepositoryIDKey(1002),
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	assert.Empty(hidden,
		"a displaced row must not receive the visibility mutation")
}

func TestHandleUpdateRepoUIVisibilityReportsRouteOnlyTrackedRef(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _, syncer := servertest.SetupTestServerWithConfig(t)

	serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Key:          platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")),
		Owner:        "acme",
		Name:         "widget",
	})
	// The tracked snapshot has not resolved a stable provider id yet; the
	// route is the only address. Hidden correlation must still report the
	// saved state instead of skipping identity-less refs.
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos := serverfake.SettingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.True(repos[0].HiddenFromUI,
		"route-only tracked refs resolve through the catalog row")

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	require.Len(hidden, 1)
	assert.Equal(platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")), hidden[0].Key)
}
