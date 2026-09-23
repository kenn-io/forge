package settingsservertest

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil"
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

func listRepoNames(t *testing.T, srv *server.Server) []string {
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

func TestHandleUpdateRepoUIVisibilityFollowsRenamedRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _, syncer := setupTestServerWithConfig(t)

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
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme-renamed",
		Name:               "widget-renamed",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_widget",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

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
	srv, database, _, syncer := setupTestServerWithConfig(t)

	// R_old was verified at acme/widget and hidden there.
	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_old",
		Owner:          "acme",
		Name:           "widget",
	})
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_old",
		ConfiguredRepoPath: "acme/widget",
	}})
	rr := testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

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
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_new",
		ConfiguredRepoPath: "acme/widget",
	}})

	rr = testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	repos := settingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.False(repos[0].HiddenFromUI,
		"replacement repo must not inherit hidden state through the reused route")
	assert.Equal([]string{"widget"}, listRepoNames(t, srv),
		"replacement repo stays in the interactive catalog")

	// Hiding the entry now targets the replacement's stable identity.
	rr = testutil.DoJSON(t, srv, http.MethodPut,
		"/api/v1/repo/github/acme/widget/ui-visibility",
		map[string]bool{"hidden": true})

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
	srv, database, _, syncer := setupTestServerWithConfig(t)

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
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "R_old",
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
	srv, database, _, syncer := setupTestServerWithConfig(t)

	seedVerifiedRepo(t, database, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "R_widget",
		Owner:          "acme",
		Name:           "widget",
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
	repos := settingsReposFromBody(t, rr.Body.Bytes())
	require.Len(repos, 1)
	assert.True(repos[0].HiddenFromUI,
		"route-only tracked refs resolve through the catalog row")

	hidden, err := database.HiddenRepos(t.Context())
	require.NoError(err)
	require.Len(hidden, 1)
	assert.Equal("R_widget", hidden[0].PlatformRepoID)
}
