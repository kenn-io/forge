package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestTeamReadsPaginateAndUseUserCredential(t *testing.T) {
	var calls []string
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RequestURI())
		auth = append(auth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", fmt.Sprintf("<http://%s%s?page=2>; rel=\"next\"", r.Host, r.URL.Path))
		}
		if r.URL.Path == "/api/v3/orgs/acme/teams" {
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `[{"slug":"two","name":"Two"}]`)
			} else {
				fmt.Fprint(w, `[{"slug":"one","name":"One"}]`)
			}
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"id":2,"full_name":"acme/two"}]`)
		} else {
			fmt.Fprint(w, `[{"id":1,"full_name":"acme/one"}]`)
		}
	}))
	defer server.Close()
	client, err := gh.NewClient(gh.WithHTTPClient(server.Client()), gh.WithEnterpriseURLs(server.URL+"/api/v3/", server.URL+"/api/uploads/"), gh.WithAuthToken("user-token"))
	require.NoError(t, err)
	c := &Client{ghWrite: client, now: time.Now}
	teams, err := c.ListOrgTeams(t.Context(), "acme")
	require.NoError(t, err)
	require.Len(t, teams, 2)
	repos, err := c.ListTeamRepositories(t.Context(), "acme", "one")
	require.NoError(t, err)
	require.Len(t, repos, 2)
	require.Equal(t, int64(2), repos[1].GetID())
	require.Len(t, calls, 4)
	require.Equal(t, []string{"Bearer user-token", "Bearer user-token", "Bearer user-token", "Bearer user-token"}, auth)
}

func TestTeamReadsMapErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		handler   http.HandlerFunc
		listRepos bool
		code      platform.PlatformErrorCode
	}{
		{"later page", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "2" {
				http.Error(w, `{"message":"denied"}`, http.StatusForbidden)
				return
			}
			w.Header().Set("Link", fmt.Sprintf("<http://%s%s?page=2>; rel=\"next\"", r.Host, r.URL.Path))
			fmt.Fprint(w, `[{"slug":"one"}]`)
		}, false, platform.ErrCodePermissionDenied},
		{"rate limit", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "2000000000")
			http.Error(w, `{"message":"rate limit"}`, http.StatusTooManyRequests)
		}, true, platform.ErrCodeRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			client, err := newEnterpriseGHClient(server.Client(), server.URL+"/api/v3/", server.URL+"/api/uploads/")
			require.NoError(t, err)
			c := &Client{ghWrite: client, now: time.Now}
			if tc.listRepos {
				_, err = c.ListTeamRepositories(t.Context(), "acme", "payments")
			} else {
				var rows []*gh.Team
				rows, err = c.ListOrgTeams(t.Context(), "acme")
				require.Nil(t, rows)
			}
			var providerErr *platform.Error
			require.ErrorAs(t, err, &providerErr)
			require.Equal(t, tc.code, providerErr.Code)
			if tc.listRepos {
				require.NotNil(t, providerErr.ResetAt)
			}
		})
	}
}
