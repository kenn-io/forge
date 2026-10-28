package githubapp_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
)

func TestCreateInstallationToken(t *testing.T) {
	for _, tt := range []struct {
		name     string
		request  *githubapp.InstallationTokenRequest
		wantBody string
	}{
		{name: "installation defaults"},
		{
			name: "repository and permission limits",
			request: &githubapp.InstallationTokenRequest{
				RepositoryIDs: []int64{91},
				Permissions:   map[string]string{"contents": "write", "pull_requests": "read"},
			},
			wantBody: `{"repository_ids":[91],"permissions":{"contents":"write","pull_requests":"read"}}`,
		},
		{
			name:     "explicit empty permissions",
			request:  &githubapp.InstallationTokenRequest{Permissions: map[string]string{}},
			wantBody: `{"permissions":{}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(http.MethodPost, r.Method)
				assert.Equal("/api/v3/app/installations/42/access_tokens", r.URL.Path)
				assert.Equal("Bearer app-jwt", r.Header.Get("Authorization"))
				body, err := io.ReadAll(r.Body)
				assert.NoError(err)
				if tt.wantBody == "" {
					assert.Empty(body)
					assert.Empty(r.Header.Get("Content-Type"))
				} else {
					assert.JSONEq(tt.wantBody, string(body))
					assert.Equal("application/json", r.Header.Get("Content-Type"))
				}
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"token":"installation-token","expires_at":"2026-01-02T03:04:05Z","permissions":{"contents":"read","metadata":"read"},"repository_selection":"selected","repositories":[{"id":91,"name":"project-a","full_name":"org-a/project-a"}]}`)
			}))
			t.Cleanup(server.Close)

			token, err := githubapp.NewClientWithBase(server.URL+"/api/v3").CreateInstallationToken(
				t.Context(), "app-jwt", 42, tt.request,
			)
			require.NoError(t, err)
			assert.Equal(&githubapp.InstallationToken{
				Token:               "installation-token",
				ExpiresAt:           time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
				Permissions:         map[string]string{"contents": "read", "metadata": "read"},
				RepositorySelection: "selected",
				Repositories: []githubapp.Repository{
					{ID: 91, Name: "project-a", FullName: "org-a/project-a"},
				},
			}, token)
		})
	}
}

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
