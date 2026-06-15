package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ghUserReposCall(pageSize, page int) string {
	return fmt.Sprintf(
		"gh api user/repos?per_page=%d&page=%d&affiliation=owner,collaborator,organization_member&sort=updated",
		pageSize, page,
	)
}

func decodeUserRepositories(
	t *testing.T, ts *httptest.Server, path string,
) (*http.Response, []byte) {
	t.Helper()
	resp := httpDo(t, ts, http.MethodGet, path, nil)
	defer resp.Body.Close()
	var buf json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&buf))
	return resp, buf
}

// TestListUserRepositories covers GET /api/v1/platform/user-repositories:
// the gh listing translates to the wire shape with snake_case fields and
// the default branch flattened.
func TestListUserRepositories(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
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

	resp, body := decodeUserRepositories(
		t, ts, "/api/v1/platform/user-repositories",
	)
	require.Equal(http.StatusOK, resp.StatusCode)

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
	require := require.New(t)

	srv, _ := setupTestServer(t)
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
		resp, _ := decodeUserRepositories(t, ts, path)
		require.Equal(http.StatusOK, resp.StatusCode, path)
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
			srv, _ := setupTestServer(t)
			runner := &recordingToolingRunner{
				errs: map[string]error{
					ghUserReposCall(100, 1): tc.err,
				},
			}
			srv.toolingRun = runner.run
			ts := httptest.NewServer(srv)
			defer ts.Close()

			resp, body := decodeUserRepositories(
				t, ts, "/api/v1/platform/user-repositories",
			)
			require.Equal(tc.wantStatus, resp.StatusCode)
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
	require := require.New(t)
	assert := assert.New(t)

	srv, _ := setupTestServer(t)
	runner := &recordingToolingRunner{}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, body := decodeUserRepositories(
		t, ts, "/api/v1/platform/user-repositories?provider=gitlab",
	)
	require.Equal(http.StatusConflict, resp.StatusCode)
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

	srv, _ := setupTestServer(t)
	runner := &recordingToolingRunner{
		outputs: map[string]string{
			hostCall(100, 1): repoJSON(100),
			hostCall(100, 2): repoJSON(20),
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, body := decodeUserRepositories(t, ts,
		"/api/v1/platform/user-repositories?limit=150&platform_host=ghe.example.com")
	require.Equal(http.StatusOK, resp.StatusCode)

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
	require := require.New(t)

	full := make([]string, 0, 100)
	for i := range 100 {
		full = append(full, fmt.Sprintf(`{"full_name":"acme/r%d"}`, i))
	}
	srv, _ := setupTestServer(t)
	runner := &recordingToolingRunner{
		outputs: map[string]string{
			ghUserReposCall(100, 1): "[" + strings.Join(full, ",") + "]",
			ghUserReposCall(100, 2): "[" + strings.Join(full[:50], ",") + "]",
		},
	}
	srv.toolingRun = runner.run
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, body := decodeUserRepositories(t, ts,
		"/api/v1/platform/user-repositories?limit=150")
	require.Equal(http.StatusOK, resp.StatusCode)
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
	require := require.New(t)
	assert := assert.New(t)

	srv, _ := setupTestServer(t)
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

	resp, body := decodeUserRepositories(t, ts,
		"/api/v1/platform/user-repositories?platform_host=ghe.example.com")
	require.Equal(http.StatusBadGateway, resp.StatusCode)
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
