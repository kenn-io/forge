package githubapp_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
)

func TestCheckInstallation(t *testing.T) {
	cases := []struct {
		name          string
		appStatus     int
		appBody       string
		installStatus int
		installBody   string
		kind          githubapp.InstallationFailure
		wantError     bool
		reads         int
	}{
		{"active", 200, `{"id":7}`, 200, `{"id":42,"app_id":7,"repository_selection":"selected","account":{"id":81}}`, "", false, 2},
		{"suspended", 200, `{"id":7}`, 200, `{"id":42,"app_id":7,"suspended_at":"2026-01-02T03:04:05Z"}`, githubapp.InstallationSuspended, true, 2},
		{"deleted", 200, `{"id":7}`, 404, `{"message":"not found"}`, githubapp.InstallationDeleted, true, 2},
		{"rejected JWT", 401, `{"message":"private response"}`, 200, `{}`, githubapp.AppAuthenticationRejected, true, 1},
		{"JWT expires between reads", 200, `{"id":7}`, 401, `{}`, githubapp.AppAuthenticationRejected, true, 2},
		{"app not found", 404, `{}`, 404, `{}`, "", true, 1},
		{"wrong app", 200, `{"id":8}`, 404, `{}`, "", true, 1},
		{"missing app ID", 200, `{}`, 404, `{}`, "", true, 1},
		{"wrong installation", 200, `{"id":7}`, 200, `{"id":43,"app_id":7}`, "", true, 2},
		{"wrong installation app", 200, `{"id":7}`, 200, `{"id":42,"app_id":8}`, "", true, 2},
		{"forbidden", 200, `{"id":7}`, 403, `{}`, "", true, 2},
		{"rate limited", 200, `{"id":7}`, 429, `{}`, "", true, 2},
		{"server failure", 200, `{"id":7}`, 503, `{}`, "", true, 2},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				assert.Equal("GET", r.Method)
				assert.Equal("Bearer app-jwt", r.Header.Get("Authorization"))
				switch r.URL.Path {
				case "/api/v3/app":
					w.WriteHeader(tt.appStatus)
					fmt.Fprint(w, tt.appBody)
				case "/api/v3/app/installations/42":
					w.WriteHeader(tt.installStatus)
					fmt.Fprint(w, tt.installBody)
				default:
					http.Error(w, "unexpected route", http.StatusBadRequest)
				}
			}))
			defer server.Close()
			installation, err := githubapp.NewClientWithBase(server.URL+"/api/v3").CheckInstallation(t.Context(), "app-jwt", 7, 42)
			if tt.wantError {
				require.Error(err)
				assert.Nil(installation)
			} else {
				require.NoError(err)
				require.NotNil(installation)
				assert.Equal(int64(42), installation.ID)
				assert.Equal(int64(81), installation.Account.ID)
				assert.Equal("selected", installation.RepositorySelection)
			}
			failure, ok := errors.AsType[*githubapp.InstallationError](err)
			if tt.kind == "" {
				assert.False(ok, "an uncertain response must not change credential lifecycle")
			} else {
				require.True(ok)
				assert.Equal(tt.kind, failure.Kind)
				assert.NotContains(err.Error(), "private response")
			}
			assert.Len(paths, tt.reads)
			assert.Equal("/api/v3/app", paths[0])
			if tt.kind == githubapp.InstallationDeleted {
				assert.True(githubapp.IsStatus(err, http.StatusNotFound))
			}
		})
	}
}
