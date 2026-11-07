package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/tokenauth"
	platformgithub "go.kenn.io/forge/platform/github"
)

// githubTokenFixture serves one GitHub host through a real API client whose
// requests reach a test provider. Every CLI is unavailable.
type githubTokenFixture struct {
	srv     *Server
	ts      *httptest.Server
	now     atomic.Int64
	ghCalls atomic.Int64
}

// newGitHubTokenFixture registers the same host and repository sources as startup.
func newGitHubTokenFixture(
	t *testing.T, cfg *config.Config,
	provider http.HandlerFunc, opts ServerOptions,
) *githubTokenFixture {
	t.Helper()
	if opts.TokenSources == nil {
		opts.TokenSources = tokenauth.NewSourceSet(tokenauth.Options{})
		for _, plan := range cfg.ProviderTokenSources() {
			opts.TokenSources.Upsert(plan.Descriptor)
		}
	}
	api := httptest.NewServer(provider)
	t.Cleanup(api.Close)
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	f := &githubTokenFixture{srv: New(database, syncer, nil, "/", cfg, opts)}
	t.Cleanup(func() { serverfake.GracefulShutdown(t, f.srv) })
	f.srv.newGitHubClient = func(source tokenauth.Source, host string) (platformgithub.UserRepositoriesAPI, error) {
		client, err := ghclient.NewClient(source, host, nil, nil, ghclient.WithBaseURLForTesting(api.URL))
		if err != nil {
			return nil, err
		}
		return client.(platformgithub.UserRepositoriesAPI), nil
	}
	f.now.Store(time.Now().Unix())
	f.srv.now = func() time.Time { return time.Unix(f.now.Load(), 0).UTC() }
	f.srv.toolingRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "gh" {
			f.ghCalls.Add(1)
		}
		return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	f.ts = httptest.NewServer(f.srv)
	t.Cleanup(f.ts.Close)
	return f
}

func TestListUserRepositoriesUsesGitHubTokenWithoutGh(t *testing.T) {
	for _, tc := range []struct {
		name            string
		platformHost    string
		repositoryToken bool
		rejected        bool
	}{
		{name: "empty repository config"},
		{name: "uppercase public host", platformHost: "GITHUB.COM"},
		{name: "public host with HTTPS port", platformHost: "github.com:443"},
		{name: "repository token differs", repositoryToken: true},
		{name: "rejected token", rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			t.Setenv("KENN_FORGE_GITHUB_TOKEN", "host-token")
			t.Setenv("TEST_REPOSITORY_TOKEN", "repository-token")
			cfg := &config.Config{GitHubTokenEnv: "KENN_FORGE_GITHUB_TOKEN"}
			if tc.repositoryToken {
				cfg.Repos = []config.Repo{{Owner: "acme", Name: "widget", TokenEnv: "TEST_REPOSITORY_TOKEN"}}
			}
			var requests atomic.Int32
			f := newGitHubTokenFixture(t, cfg, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal("/api/v3/user/repos", r.URL.Path)
				assert.Equal("Bearer host-token", r.Header.Get("Authorization"))
				assert.Equal("owner,collaborator,organization_member", r.URL.Query().Get("affiliation"))
				assert.Equal("updated", r.URL.Query().Get("sort"))
				assert.Equal("1", r.URL.Query().Get("page"))
				assert.Equal("100", r.URL.Query().Get("per_page"))
				if tc.rejected {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
					return
				}
				_, _ = w.Write([]byte(`[{"full_name":"acme/widget","ssh_url":"git@github.com:acme/widget.git","default_branch":"main","private":true,"visibility":"private"}]`))
			}, ServerOptions{})
			newClient := f.srv.newGitHubClient
			f.srv.newGitHubClient = func(source tokenauth.Source, host string) (platformgithub.UserRepositoriesAPI, error) {
				// The test endpoint override would otherwise hide an Enterprise route.
				assert.Equal("github.com", host)
				return newClient(source, host)
			}
			status, body := decodeUserRepositories(t, f.ts, "/api/v1/platform/user-repositories?platform_host="+tc.platformHost)
			assert.Zero(f.ghCalls.Load(), "discovery uses the token without invoking gh")
			assert.Positive(requests.Load(), "discovery reaches the provider")
			if tc.rejected {
				assert.Equal(http.StatusForbidden, status)
				assert.Contains(string(body), `"code":"forbidden"`)
				return
			}
			require.Equal(http.StatusOK, status)
			var decoded struct {
				Repositories json.RawMessage `json:"repositories"`
			}
			require.NoError(json.Unmarshal(body, &decoded))
			assert.JSONEq(`[{"name_with_owner":"acme/widget","ssh_url":"git@github.com:acme/widget.git","default_branch":"main","private":true}]`, string(decoded.Repositories))
		})
	}
}

func ghUserReposCall(pageSize, page int) string {
	return fmt.Sprintf(
		"gh api user/repos?per_page=%d&page=%d&affiliation=owner,collaborator,organization_member&sort=updated",
		pageSize, page,
	)
}

func decodeUserRepositories(
	t *testing.T, ts *httptest.Server, path string,
) (int, []byte) {
	t.Helper()
	resp := serverfake.HttpDo(t, ts, http.MethodGet, path, nil)
	defer resp.Body.Close()
	var buf json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&buf))
	return resp.StatusCode, buf
}

// TestListUserRepositories covers GET /api/v1/platform/user-repositories:
// the gh listing translates to the wire shape with snake_case fields and
// the default branch flattened.
func TestListUserRepositories(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	srv, _, _ := setupTestServer(t)
	runner := &recordingToolingRunner{
		outputs: map[string]string{
			ghUserReposCall(100, 1): `[
				{"full_name":"acme/widget","ssh_url":"git@github.com:acme/widget.git","default_branch":"main"},
				{"full_name":"acme/tools","ssh_url":"git@github.com:acme/tools.git","default_branch":""}
			]`,
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	status, body := decodeUserRepositories(
		t, ts, "/api/v1/platform/user-repositories",
	)
	require.Equal(http.StatusOK, status)

	var decoded struct {
		Repositories []struct {
			NameWithOwner string `json:"name_with_owner"`
			SSHURL        string `json:"ssh_url"`
			DefaultBranch string `json:"default_branch"`
		} `json:"repositories"`
	}
	require.NoError(json.Unmarshal(body, &decoded))
	require.Len(decoded.Repositories, 2)
	assert.Equal("acme/widget", decoded.Repositories[0].NameWithOwner)
	assert.Equal("git@github.com:acme/widget.git", decoded.Repositories[0].SSHURL)
	assert.Equal("main", decoded.Repositories[0].DefaultBranch)
	assert.Empty(decoded.Repositories[1].DefaultBranch)
}

// TestListUserRepositoriesClampsLimit pins the limit fallback: zero
// and limits beyond the 1000 cap fall back to the default of 100.
func TestListUserRepositoriesClampsLimit(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	srv, _, _ := setupTestServer(t)
	runner := &recordingToolingRunner{
		outputs: map[string]string{
			ghUserReposCall(100, 1): "[]",
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	for _, path := range []string{
		"/api/v1/platform/user-repositories",
		"/api/v1/platform/user-repositories?limit=5",
		"/api/v1/platform/user-repositories?limit=5000",
	} {
		status, _ := decodeUserRepositories(t, ts, path)
		require.Equal(http.StatusOK, status, path)
	}
	// per_page stays constant at 100 regardless of limit; small
	// limits truncate after the fetch.
	require.Equal([]string{
		ghUserReposCall(100, 1),
		ghUserReposCall(100, 1),
		ghUserReposCall(100, 1),
	}, runner.calls)
}

// TestListUserRepositoriesProblemCodes pins the recovery contract: a
// missing gh binary and an unauthenticated gh map to distinct problem
// codes so the UI can offer the right fix.
func TestListUserRepositoriesProblemCodes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name: "missing gh",
			err: &exec.Error{
				Name: "gh", Err: exec.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   "toolMissing",
		},
		{
			name:       "unauthenticated",
			err:        errors.New("gh: To get started with GitHub CLI, please run: gh auth login"),
			wantStatus: http.StatusForbidden,
			wantCode:   "toolUnauthenticated",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := setupTestServer(t)
			runner := &recordingToolingRunner{
				errs: map[string]error{
					ghUserReposCall(100, 1): tc.err,
				},
			}
			srv.toolingRun = runner.run
			ts := httptest.NewServer(srv)
			defer ts.Close()

			status, body := decodeUserRepositories(
				t, ts, "/api/v1/platform/user-repositories",
			)
			require.Equal(tc.wantStatus, status)
			var problem struct {
				Code string `json:"code"`
			}
			require.NoError(json.Unmarshal(body, &problem))
			assert.Equal(tc.wantCode, problem.Code)
		})
	}
}

// TestListUserRepositoriesRejectsUnimplementedProvider pins the
// provider contract: the endpoint is provider-aware in shape, and an
// unimplemented provider gets a typed unsupportedCapability problem
// instead of silently listing the wrong platform.
func TestListUserRepositoriesRejectsUnimplementedProvider(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, _, _ := setupTestServer(t)
	runner := &recordingToolingRunner{}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	status, body := decodeUserRepositories(
		t, ts, "/api/v1/platform/user-repositories?provider=gitlab",
	)
	require.Equal(http.StatusConflict, status)
	var problem struct {
		Code    string `json:"code"`
		Details struct {
			Capability string `json:"capability"`
		} `json:"details"`
	}
	require.NoError(json.Unmarshal(body, &problem))
	assert.Equal("unsupportedCapability", problem.Code)
	assert.Equal("user-repositories", problem.Details.Capability)
	assert.Empty(runner.calls, "no subprocess for an unimplemented provider")
}

// TestListUserRepositoriesPaginatesAndTargetsHost pins pagination and
// host targeting: limits beyond one page walk successive pages until a
// short page, and a platform_host routes every page through gh's
// --hostname so self-hosted deployments list their own repositories.
func TestListUserRepositoriesPaginatesAndTargetsHost(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	repoJSON := func(n int) string {
		items := make([]string, 0, n)
		for i := range n {
			items = append(items, fmt.Sprintf(
				`{"full_name":"acme/r%d","ssh_url":"git@ghe.example.com:acme/r%d.git","default_branch":"main"}`,
				i, i,
			))
		}
		return "[" + strings.Join(items, ",") + "]"
	}
	hostCall := func(pageSize, page int) string {
		return fmt.Sprintf(
			"gh api --hostname ghe.example.com user/repos?per_page=%d&page=%d&affiliation=owner,collaborator,organization_member&sort=updated",
			pageSize, page,
		)
	}

	srv, _, _ := setupTestServer(t)
	runner := &recordingToolingRunner{
		outputs: map[string]string{
			hostCall(100, 1): repoJSON(100),
			hostCall(100, 2): repoJSON(20),
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	status, body := decodeUserRepositories(t, ts,
		"/api/v1/platform/user-repositories?limit=150&platform_host=ghe.example.com")
	require.Equal(http.StatusOK, status)

	var decoded struct {
		Repositories []struct {
			NameWithOwner string `json:"name_with_owner"`
		} `json:"repositories"`
	}
	require.NoError(json.Unmarshal(body, &decoded))
	assert.Len(decoded.Repositories, 120,
		"page 1 (100) plus the short page 2 (20)")
	require.Equal([]string{hostCall(100, 1), hostCall(100, 2)}, runner.calls)
}

// TestListUserRepositoriesTruncatesMidPageLimit pins the constant
// per_page contract: a limit that ends mid-page keeps per_page at 100
// (page offsets are per_page-relative) and truncates the appended
// results instead of shrinking the final request.
func TestListUserRepositoriesTruncatesMidPageLimit(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	full := make([]string, 0, 100)
	for i := range 100 {
		full = append(full, fmt.Sprintf(`{"full_name":"acme/r%d"}`, i))
	}
	srv, _, _ := setupTestServer(t)
	runner := &recordingToolingRunner{
		outputs: map[string]string{
			ghUserReposCall(100, 1): "[" + strings.Join(full, ",") + "]",
			ghUserReposCall(100, 2): "[" + strings.Join(full[:50], ",") + "]",
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	status, body := decodeUserRepositories(t, ts,
		"/api/v1/platform/user-repositories?limit=150")
	require.Equal(http.StatusOK, status)
	var decoded struct {
		Repositories []struct {
			NameWithOwner string `json:"name_with_owner"`
		} `json:"repositories"`
	}
	require.NoError(json.Unmarshal(body, &decoded))
	require.Len(decoded.Repositories, 150)
	require.Equal("acme/r49", decoded.Repositories[149].NameWithOwner,
		"the tail must come from page 2, not a re-fetched overlap")
	require.Equal([]string{
		ghUserReposCall(100, 1),
		ghUserReposCall(100, 2),
	}, runner.calls)
}

// TestListUserRepositoriesUpstreamErrorCarriesHost pins the host-aware
// upstream contract: a generic gh failure against a platform_host
// surfaces as a 502 upstreamError whose details carry that host.
func TestListUserRepositoriesUpstreamErrorCarriesHost(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, _, _ := setupTestServer(t)
	runner := &recordingToolingRunner{
		errs: map[string]error{
			"gh api --hostname ghe.example.com user/repos?per_page=100&page=1&affiliation=owner,collaborator,organization_member&sort=updated": errors.New(
				"unexpected end of JSON input",
			),
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	status, body := decodeUserRepositories(t, ts,
		"/api/v1/platform/user-repositories?platform_host=ghe.example.com")
	require.Equal(http.StatusBadGateway, status)
	var problem struct {
		Code    string `json:"code"`
		Details struct {
			PlatformHost string `json:"platformHost"`
		} `json:"details"`
	}
	require.NoError(json.Unmarshal(body, &problem))
	assert.Equal("upstreamError", problem.Code)
	assert.Equal("ghe.example.com", problem.Details.PlatformHost)
}
