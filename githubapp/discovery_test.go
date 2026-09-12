package githubapp_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
)

func TestInstallationDiscoveryIsPaged(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal("/api/v3/app/installations", r.URL.Path)
		assert.Equal("Bearer app-jwt", r.Header.Get("Authorization"))
		assert.Equal("1", r.URL.Query().Get("per_page"))
		switch r.URL.Query().Get("page") {
		case "2":
			fmt.Fprint(w, `[{"id":42,"app_id":7,"account":{"id":81,"login":"org-a","type":"Organization"},"repository_selection":"all","suspended_at":"2026-01-02T03:04:05Z"}]`)
		case "3":
			fmt.Fprint(w, `[]`)
		default:
			http.Error(w, "wrong page", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := githubapp.NewClientWithBase(server.URL + "/api/v3")
	page, err := client.ListInstallationsPage(t.Context(), "app-jwt", 2, 1)
	require.NoError(err)
	require.Len(page.Installations, 1)
	assert.Equal(1, requests)
	assert.Equal(3, page.NextPage)
	installation := page.Installations[0]
	assert.Equal(int64(42), installation.ID)
	assert.Equal(int64(7), installation.AppID)
	assert.Equal(int64(81), installation.Account.ID)
	assert.Equal("org-a", installation.Account.Login)
	assert.Equal("all", installation.RepositorySelection)
	require.NotNil(installation.SuspendedAt)
	assert.Equal(int64(1767323045), installation.SuspendedAt.Unix())
	last, err := client.ListInstallationsPage(t.Context(), "app-jwt", page.NextPage, 1)
	require.NoError(err)
	assert.Empty(last.Installations)
	assert.Zero(last.NextPage)
}

func TestRepositoryDiscoveryPreservesIdentity(t *testing.T) {
	assert := assert.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/installation/repositories", r.URL.Path)
		assert.Equal("Bearer installation-token", r.Header.Get("Authorization"))
		assert.Equal("4", r.URL.Query().Get("page"))
		assert.Equal("2", r.URL.Query().Get("per_page"))
		fmt.Fprint(w, `{"total_count":7,"repositories":[{"id":91,"name":"project-a","full_name":"org-a/project-a","owner":{"id":81,"login":"org-a","type":"Organization"},"default_branch":"trunk","private":true}]}`)
	}))
	defer server.Close()
	page, err := githubapp.NewClientWithBase(server.URL).ListInstallationRepositoriesPage(t.Context(), "installation-token", 4, 2)
	require.NoError(t, err)
	require.Len(t, page.Repositories, 1)
	assert.Zero(page.NextPage)
	assert.Equal(githubapp.Repository{
		ID: 91, Name: "project-a", FullName: "org-a/project-a",
		Owner:         githubapp.Account{ID: 81, Login: "org-a", Type: "Organization"},
		DefaultBranch: "trunk", Private: true,
	}, page.Repositories[0])
}

func TestDiscoveryRejectsInvalidBounds(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	client := githubapp.NewClientWithBase(server.URL)
	for _, bounds := range [][2]int{{0, 1}, {-1, 1}, {1, 0}, {1, 101}} {
		_, err := client.ListInstallationsPage(t.Context(), "app-jwt", bounds[0], bounds[1])
		require.Error(t, err)
		_, err = client.ListInstallationRepositoriesPage(t.Context(), "installation-token", bounds[0], bounds[1])
		require.Error(t, err)
	}
	assert.Zero(t, requests)
}

func TestDiscoveryRejectsOversizedPage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations" {
			fmt.Fprint(w, `[{"id":1},{"id":2}]`)
			return
		}
		fmt.Fprint(w, `{"repositories":[{"id":1},{"id":2}]}`)
	}))
	defer server.Close()
	client := githubapp.NewClientWithBase(server.URL)
	installations, err := client.ListInstallationsPage(t.Context(), "app-jwt", 1, 1)
	require.Error(err)
	assert.Empty(installations.Installations)
	repositories, err := client.ListInstallationRepositoriesPage(t.Context(), "installation-token", 1, 1)
	require.Error(err)
	assert.Empty(repositories.Repositories)
}

func TestDiscoveryDoesNotTurnUnreadablePagesIntoEmptyInventory(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"repositories":null}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			client := githubapp.NewClientWithBase(server.URL)
			_, err := client.ListInstallationsPage(t.Context(), "app-jwt", 1, 10)
			require.Error(t, err)
			_, err = client.ListInstallationRepositoriesPage(t.Context(), "installation-token", 1, 10)
			require.Error(t, err)
		})
	}
}
