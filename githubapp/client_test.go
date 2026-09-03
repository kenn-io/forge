package githubapp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
)

func TestListInstallationsPaginates(t *testing.T) {
	for _, count := range []int{0, 1, 100, 101, 200} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal("/api/v3/app/installations", r.URL.Path)
				assert.Equal("100", r.URL.Query().Get("per_page"))
				assert.Equal("Bearer app-jwt", r.Header.Get("Authorization"))
				page, err := strconv.Atoi(r.URL.Query().Get("page"))
				if err != nil || page < 1 {
					// GitHub defaults an omitted page to the first page.
					page = 1
				}
				out := make([]githubapp.Installation, 0)
				for id := (page-1)*100 + 1; id <= min(page*100, count); id++ {
					out = append(out, githubapp.Installation{ID: int64(id)})
				}
				assert.NoError(json.NewEncoder(w).Encode(out))
			}))
			defer server.Close()
			client := githubapp.NewClientWithBase(server.URL + "/api/v3")
			installs, err := client.ListInstallations(t.Context(), "app-jwt")
			require.NoError(err)
			require.Len(installs, count)
			for i, installation := range installs {
				require.Equal(int64(i+1), installation.ID)
			}
		})
	}
}

func TestListInstallationsRejectsIncompleteInventory(t *testing.T) {
	assert := assert.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			http.Error(w, "installation listing unavailable", http.StatusServiceUnavailable)
			return
		}
		out := make([]githubapp.Installation, 100)
		for i := range out {
			out[i].ID = int64(i + 1)
		}
		assert.NoError(json.NewEncoder(w).Encode(out))
	}))
	defer server.Close()
	installs, err := githubapp.NewClientWithBase(server.URL).ListInstallations(t.Context(), "app-jwt")
	require.Error(t, err)
	assert.True(githubapp.IsStatus(err, http.StatusServiceUnavailable))
	assert.Nil(installs, "a failed page must not look like a complete inventory")
}
