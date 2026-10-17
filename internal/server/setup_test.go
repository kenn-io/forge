package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/federationtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/serverfake"
)

func TestSetupWorkFromWorkspace(t *testing.T) {
	for _, status := range []string{"ready", "creating", "error", "deleting", "deletion_failed"} {
		t.Run(status, func(t *testing.T) {
			require := require.New(t)
			database := dbtest.Open(t)
			require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
				ID: "example-workspace", ItemKey: "example-work", RepoOwner: "org-a", RepoName: "project-a",
				ItemType: db.WorkspaceItemTypeAdHoc, GitHeadRef: "main", WorktreePath: t.TempDir(), Status: status,
			}))
			srv := New(database, nil, nil, "/", nil, ServerOptions{DisableWorkspaceBackgroundMonitors: true})
			t.Cleanup(func() { require.NoError(srv.Shutdown(context.Background())) })
			readable, err := srv.HasSetupWork(t.Context())
			require.NoError(err)
			assert.Equal(t, status == "ready", readable)
		})
	}
}

func TestHasSetupWorkBehindUntrackedRepository(t *testing.T) {
	for _, source := range []string{"local", "spoke"} {
		for _, kind := range []string{"pr", "issue"} {
			t.Run(source+"/"+kind, func(t *testing.T) {
				require := require.New(t)
				database := dbtest.Open(t)
				now := time.Now().UTC().Truncate(time.Second)
				for i, name := range []string{"tracked", "retained"} {
					repoID, err := reposeed.Seed(t.Context(), database, serverfake.VerifiedGitHubRepoIdentity("github.com", "org-a", name))
					require.NoError(err)
					if name == "tracked" {
						require.NoError(database.SetRepoHiddenFromUI(t.Context(), repoID, true))
					}
					activity := now.Add(time.Duration(i-1) * time.Hour)
					if kind == "pr" {
						serverfake.SeedPRForRepo(t, database, repoID, "github.com", "org-a", name, 42, serverfake.WithSeedPRTimes(activity, activity, activity))
					} else {
						serverfake.SeedIssueForRepo(t, database, repoID, "github.com", "org-a", name, 42, "open", "Example issue")
						issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 42)
						require.NoError(err)
						require.NotNil(issue)
						issue.CreatedAt, issue.UpdatedAt, issue.LastActivityAt = activity, activity, activity
						_, err = database.UpsertIssue(t.Context(), issue)
						require.NoError(err)
					}
				}
				cfg := &config.Config{Repos: []config.Repo{{Owner: "org-a", Name: "tracked"}}}
				syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": &serverfake.MockGH{}}, database, nil, []ghclient.RepoRef{{Owner: "org-a", Name: "tracked"}}, time.Minute, nil, nil)
				t.Cleanup(syncer.Stop)
				srv := New(database, syncer, nil, "/", cfg, ServerOptions{DisableWorkspaceBackgroundMonitors: true})
				t.Cleanup(func() { require.NoError(srv.Shutdown(context.Background())) })
				reader := srv
				if source == "spoke" {
					hub := httptest.NewTLSServer(srv)
					t.Cleanup(hub.Close)
					reader, _ = newFederatedProviderNodeForTest(t, "55555555555555555555555555555555", hub.URL, hub.Client())
				}
				// Hidden repositories still contribute work to public item lists.
				path := "/api/v1/pulls?state=all&limit=1"
				if kind == "issue" {
					path = "/api/v1/issues?state=all&limit=1"
				}
				response := httptest.NewRecorder()
				reader.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:8080"+path, nil))
				require.Equal(http.StatusOK, response.Code)
				var page []struct {
					Repo struct {
						Name string `json:"name"`
					} `json:"repo"`
				}
				require.NoError(json.Unmarshal(response.Body.Bytes(), &page))
				require.Len(page, 1)
				require.Equal("tracked", page[0].Repo.Name)
				readable, err := reader.HasSetupWork(t.Context())
				require.NoError(err)
				assert.True(t, readable, "the older tracked item remains readable behind newer retained untracked work")
			})
		}
	}
}

func TestHasSetupWorkWithNoTrackedRepositories(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	serverfake.SeedPR(t, database, "org-a", "retained", 42)
	serverfake.SeedIssue(t, database, "org-a", "retained", 43, "open")
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", &config.Config{}, ServerOptions{DisableWorkspaceBackgroundMonitors: true})
	t.Cleanup(func() { require.NoError(srv.Shutdown(context.Background())) })
	readable, err := srv.HasSetupWork(t.Context())
	require.NoError(err)
	assert.False(t, readable)
}

func TestHasSetupWorkFromExecutionWorker(t *testing.T) {
	database := dbtest.Open(t)
	srv := New(database, nil, nil, "/", nil, ServerOptions{
		ExecutionWorker: true, DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.Background())) })

	readable, err := srv.HasSetupWork(t.Context())
	require.NoError(t, err)
	require.False(t, readable)
}

func TestHasSetupWorkFromSpokeProviderSource(t *testing.T) {
	for _, test := range []struct {
		kind   string
		active bool
	}{
		{"pr", true},
		{"issue", true},
		{"inactive", false},
	} {
		t.Run(test.kind, func(t *testing.T) {
			require := require.New(t)
			database := dbtest.Open(t)
			var requests atomic.Int64
			hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if test.kind == "pr" && r.URL.Path == "/api/v1/pulls" || test.kind == "issue" && r.URL.Path == "/api/v1/issues" {
					_, _ = io.WriteString(w, `[{"number":42,"title":"Example","state":"open","repo":{"provider":"github","platform_host":"github.com","owner":"user-a","name":"project-a"}}]`)
					return
				}
				_, _ = io.WriteString(w, `[]`)
			}))
			t.Cleanup(hub.Close)
			const hubID = "44444444444444444444444444444444"
			cfg, credentials := federationtest.ProviderSpoke(t, hubID, hub.URL, "example-secret")
			srv := New(database, nil, nil, "/", cfg, ServerOptions{
				FederationSpokeID: "33333333333333333333333333333333", FederationSpokeActive: test.active,
				FederationCredentials: credentials, FederationHTTPClient: hub.Client(), DisableWorkspaceBackgroundMonitors: true,
			})
			t.Cleanup(func() { require.NoError(srv.Shutdown(context.Background())) })
			readable, err := srv.HasSetupWork(t.Context())
			require.NoError(err)
			require.Equal(test.active, readable)
			if test.active {
				require.Positive(requests.Load())
			} else {
				require.Zero(requests.Load())
			}
		})
	}
}

func TestHasSetupWorkIndependentHubSources(t *testing.T) {
	for _, mode := range []string{"issue", "both-fail", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			require := require.New(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var issueRequests atomic.Int64
			hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/pulls":
					if mode == "canceled" {
						cancel()
					}
					http.Error(w, "pull source unavailable", http.StatusServiceUnavailable)
				case "/api/v1/issues":
					issueRequests.Add(1)
					if mode == "both-fail" {
						http.Error(w, "issue source unavailable", http.StatusBadGateway)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `[{"number":42,"title":"Example issue","state":"open","repo":{"provider":"github","platform_host":"github.com","owner":"org-a","name":"project-a"}}]`)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(hub.Close)
			srv, _ := newFederatedProviderNodeForTest(t, proxyTestNodeID, hub.URL, hub.Client())
			readable, err := srv.HasSetupWork(ctx)
			switch mode {
			case "issue":
				require.NoError(err)
				require.True(readable)
				require.Equal(int64(1), issueRequests.Load())
			case "both-fail":
				require.False(readable)
				require.Error(err)
				joined, ok := err.(interface{ Unwrap() []error })
				require.True(ok)
				require.Len(joined.Unwrap(), 2)
				require.Equal(int64(1), issueRequests.Load())
			case "canceled":
				require.False(readable)
				require.ErrorIs(err, context.Canceled)
				require.Zero(issueRequests.Load())
			}
		})
	}
}
