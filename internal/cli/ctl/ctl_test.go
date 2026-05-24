package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	configpkg "go.kenn.io/middleman/internal/config"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestDefaultServerComesFromMiddlemanConfig(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_HOME", t.TempDir())
	require.NoError(os.WriteFile(configpkg.DefaultConfigPath(), []byte(`
host = "127.0.0.1"
port = 8123
`), 0o600))
	var got struct {
		url string
	}
	cmd := newCommand(commandDeps{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Restish: func(_ context.Context, _ cliConfig, _ string, requestURL string, _ []string) ([]byte, error) {
			got.url = requestURL
			return nil, nil
		},
	})
	cmd.SetArgs([]string{"repos"})

	require.NoError(cmd.Execute())

	assert.Equal("http://127.0.0.1:8123/api/v1/repos", got.url)
}

func TestRootHelpPointsAgentsToQuickstartAndAPI(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newCommand(commandDeps{
		Stdout: &stdout,
		Stderr: &stderr,
		Restish: func(context.Context, cliConfig, string, string, []string) ([]byte, error) {
			return nil, nil
		},
	})
	cmd.SetArgs([]string{"--help"})

	err := cmd.Execute()

	require.NoError(err)
	help := stdout.String()
	assert.Contains(help, "quickstart")
	assert.Contains(help, "api METHOD PATH")
	assert.Contains(help, "--output")
	assert.Contains(help, "jsonl")
	assert.Empty(stderr.String())
}

func TestQuickstartFormatsStructuredOutput(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var jsonOut bytes.Buffer
	cmd := newCommand(commandDeps{
		Stdout: &jsonOut,
		Stderr: &bytes.Buffer{},
	})
	cmd.SetArgs([]string{"--server", "http://middleman.test", "quickstart"})

	require.NoError(cmd.Execute())

	var payload map[string]any
	require.NoError(json.Unmarshal(jsonOut.Bytes(), &payload))
	assert.Equal("http://middleman.test/api/v1", payload["api_base_url"])
	assert.Contains(jsonOut.String(), "middleman api GET /pulls")
	assert.Contains(jsonOut.String(), "middleman api GET /version")

	var yamlOut bytes.Buffer
	cmd = newCommand(commandDeps{
		Stdout: &yamlOut,
		Stderr: &bytes.Buffer{},
	})
	cmd.SetArgs([]string{"--server", "http://middleman.test", "--output", "yaml", "quickstart"})

	require.NoError(cmd.Execute())
	assert.Contains(yamlOut.String(), "api_base_url: http://middleman.test/api/v1")
	assert.Contains(yamlOut.String(), "middleman api GET /sync/status")

	var jsonlOut bytes.Buffer
	cmd = newCommand(commandDeps{
		Stdout: &jsonlOut,
		Stderr: &bytes.Buffer{},
	})
	cmd.SetArgs([]string{"--server", "http://middleman.test", "--output", "jsonl", "quickstart"})

	require.NoError(cmd.Execute())
	lines := strings.Split(strings.TrimSpace(jsonlOut.String()), "\n")
	require.Len(lines, 1)
	assert.Contains(lines[0], `"api_base_url":"http://middleman.test/api/v1"`)
	assert.Contains(lines[0], `"jsonl"`)
}

func TestPullsCommandDelegatesToRestishWithAgentFriendlyDefaults(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got struct {
		cfg      cliConfig
		method   string
		url      string
		bodyArgs []string
	}
	var stdout bytes.Buffer
	cmd := newCommand(commandDeps{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		Restish: func(_ context.Context, cfg cliConfig, method, requestURL string, bodyArgs []string) ([]byte, error) {
			got.cfg = cfg
			got.method = method
			got.url = requestURL
			got.bodyArgs = append([]string(nil), bodyArgs...)
			return []byte(`[{"number":7,"title":"ready"}]`), nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://middleman.test",
		"--output", "yaml",
		"pulls",
		"--state", "open",
		"--limit", "5",
		"--starred",
	})

	require.NoError(cmd.Execute())

	assert.Equal("yaml", got.cfg.output)
	assert.Equal(30*time.Second, got.cfg.timeout)
	assert.Equal(http.MethodGet, got.method)
	requestURL, err := url.Parse(got.url)
	require.NoError(err)
	assert.Equal("http://middleman.test/api/v1/pulls", requestURL.Scheme+"://"+requestURL.Host+requestURL.Path)
	values := requestURL.Query()
	assert.Equal("open", values.Get("state"))
	assert.Equal("5", values.Get("limit"))
	assert.Equal("true", values.Get("starred"))
	assert.Empty(got.bodyArgs)
	assert.Contains(stdout.String(), "number: 7")
	assert.NotContains(stdout.String(), "Content-Type")
}

func TestRawAPICommandBuildsMiddlemanAPIURLAndBodyArgs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got struct {
		method   string
		url      string
		bodyArgs []string
	}
	cmd := newCommand(commandDeps{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Restish: func(_ context.Context, _ cliConfig, method, requestURL string, bodyArgs []string) ([]byte, error) {
			got.method = method
			got.url = requestURL
			got.bodyArgs = append([]string(nil), bodyArgs...)
			return nil, nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://middleman.test/",
		"api", "POST", "/pulls/gh/acme/widget/7/comments", "body: LGTM",
	})

	require.NoError(cmd.Execute())
	assert.Equal(http.MethodPost, got.method)
	assert.Equal("http://middleman.test/api/v1/pulls/gh/acme/widget/7/comments", got.url)
	assert.Equal([]string{"body: LGTM"}, got.bodyArgs)
}

func TestRawAPICommandRejectsAbsoluteURLs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	called := false
	cmd := newCommand(commandDeps{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Restish: func(context.Context, cliConfig, string, string, []string) ([]byte, error) {
			called = true
			return nil, nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://middleman.test/",
		"api", "GET", "http://169.254.169.254/latest/meta-data",
	})

	err := cmd.Execute()

	require.Error(err)
	assert.Contains(err.Error(), "absolute API URLs are not allowed")
	assert.False(called)
}

func TestRawAPICommandWritesErrorResponseBody(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var stdout bytes.Buffer
	cmd := newCommand(commandDeps{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		Restish: func(context.Context, cliConfig, string, string, []string) ([]byte, error) {
			return []byte(`{"code":"not_found","details":{"repo":"acme/widget"}}`),
				errors.New("middleman API returned 404 Not Found")
		},
	})
	cmd.SetArgs([]string{"--server", "http://middleman.test/", "api", "GET", "/missing"})

	err := cmd.Execute()

	require.Error(err)
	assert.JSONEq(`{"code":"not_found","details":{"repo":"acme/widget"}}`, stdout.String())
}

func TestRawAPICommandRejectsDotSegmentPaths(t *testing.T) {
	for _, rawPath := range []string{
		"/../version",
		"/%2e%2e/version",
		"/%2f..%2f..%2fversion",
		"/api/v1/../version",
		"/api/v1/%2e%2e/version",
		"/api/v1/%2f..%2fversion",
	} {
		t.Run(rawPath, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			called := false
			cmd := newCommand(commandDeps{
				Stdout: &bytes.Buffer{},
				Stderr: &bytes.Buffer{},
				Restish: func(context.Context, cliConfig, string, string, []string) ([]byte, error) {
					called = true
					return nil, nil
				},
			})
			cmd.SetArgs([]string{"--server", "http://middleman.test/", "api", "GET", rawPath})

			err := cmd.Execute()

			require.Error(err)
			assert.Contains(err.Error(), "API path")
			assert.False(called)
		})
	}
}

func TestRawAPICommandAllowsEncodedSlashesInRouteParameters(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got struct {
		method string
		url    string
	}
	cmd := newCommand(commandDeps{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Restish: func(_ context.Context, _ cliConfig, method, requestURL string, _ []string) ([]byte, error) {
			got.method = method
			got.url = requestURL
			return nil, nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://middleman.test/",
		"api", "GET", "/host/gitlab.example.com/pulls/gl/Group%2FSubGroup/project/7",
	})

	require.NoError(cmd.Execute())
	assert.Equal(http.MethodGet, got.method)
	assert.Equal(
		"http://middleman.test/api/v1/host/gitlab.example.com/pulls/gl/Group%2FSubGroup/project/7",
		got.url,
	)
}

func TestPullGetAllowsNestedOwners(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got struct {
		method string
		url    string
	}
	cmd := newCommand(commandDeps{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Restish: func(_ context.Context, _ cliConfig, method, requestURL string, _ []string) ([]byte, error) {
			got.method = method
			got.url = requestURL
			return nil, nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://middleman.test/",
		"pulls", "get", "--host", "gitlab.example.com", "gl", "Group/SubGroup", "project", "7",
	})

	require.NoError(cmd.Execute())
	assert.Equal(http.MethodGet, got.method)
	assert.Equal(
		"http://middleman.test/api/v1/host/gitlab.example.com/pulls/gl/Group%2FSubGroup/project/7",
		got.url,
	)
}

func TestAPIListCommandDiscoversOpenAPIOperations(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got struct {
		method   string
		url      string
		bodyArgs []string
	}
	var stdout bytes.Buffer
	cmd := newCommand(commandDeps{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		Restish: func(_ context.Context, _ cliConfig, method, requestURL string, bodyArgs []string) ([]byte, error) {
			got.method = method
			got.url = requestURL
			got.bodyArgs = append([]string(nil), bodyArgs...)
			return []byte(`{
				"openapi": "3.1.0",
				"paths": {
					"/pulls": {
						"parameters": [
							{"name": "shared", "in": "query"}
						],
						"get": {
							"operationId": "list-pulls",
							"summary": "List pulls",
							"parameters": [
								{"name": "limit", "in": "query"},
								{"name": "repo", "in": "query"}
							]
						}
					},
					"/pulls/{provider}/{owner}/{name}/{number}": {
						"get": {
							"operationId": "get-pull",
							"summary": "Get pull",
							"parameters": [
								{"name": "provider", "in": "path"},
								{"name": "owner", "in": "path"},
								{"name": "name", "in": "path"},
								{"name": "number", "in": "path"}
							]
						}
					}
				}
			}`), nil
		},
	})
	cmd.SetArgs([]string{"--server", "http://middleman.test", "--output", "jsonl", "api", "list"})

	require.NoError(cmd.Execute())

	assert.Equal(http.MethodGet, got.method)
	assert.Equal("http://middleman.test/api/v1/openapi.json", got.url)
	assert.Empty(got.bodyArgs)
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	require.Len(lines, 2)
	assert.JSONEq(`{"method":"GET","path":"/pulls","operation_id":"list-pulls","summary":"List pulls","query_params":["limit","repo"]}`, lines[0])
	assert.JSONEq(`{"method":"GET","path":"/pulls/{provider}/{owner}/{name}/{number}","operation_id":"get-pull","summary":"Get pull"}`, lines[1])
	assert.NotContains(lines[1], "path_params")
}

func TestMiddlemanctlCommandsUseRealAPIAndSQLite(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts := setupMiddlemanctlE2E(t)

	pullsOut := runMiddleman(t, ts.URL, "--output", "jsonl", "pulls", "--limit", "2")
	pullLines := strings.Split(strings.TrimSpace(pullsOut), "\n")
	require.Len(pullLines, 2)
	assert.NotZero(jsonNumberField(t, pullLines[0]))
	assert.NotZero(jsonNumberField(t, pullLines[1]))

	issuesOut := runMiddleman(t, ts.URL, "--output", "jsonl", "issues", "--limit", "2")
	issueLines := strings.Split(strings.TrimSpace(issuesOut), "\n")
	require.Len(issueLines, 2)
	assert.NotZero(jsonNumberField(t, issueLines[0]))
	assert.NotZero(jsonNumberField(t, issueLines[1]))

	versionOut := runMiddleman(t, ts.URL, "api", "GET", "/version")
	assert.Contains(versionOut, `"version"`)

	apiListOut := runMiddleman(t, ts.URL, "--output", "jsonl", "api", "list")
	assert.Contains(apiListOut, `"method":"GET"`)
	assert.Contains(apiListOut, `"path":"/pulls"`)

	syncOut := runMiddleman(t, ts.URL, "sync")
	assert.Empty(syncOut)
}

func setupMiddlemanctlE2E(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("MIDDLEMAN_RESTISH_CONFIG_DIR", t.TempDir())
	t.Setenv("MIDDLEMAN_RESTISH_CACHE_DIR", t.TempDir())

	database := dbtest.Open(t)
	_, err := testutil.SeedFixtures(t.Context(), database)
	require.NoError(t, err)

	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, srv.Shutdown(ctx))
	})

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func runMiddleman(t *testing.T, serverURL string, args ...string) string {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newCommand(commandDeps{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	cmd.SetArgs(append([]string{"--server", serverURL}, args...))
	require.NoError(t, cmd.Execute(), stderr.String())
	return stdout.String()
}

func jsonNumberField(t *testing.T, raw string) float64 {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	for _, field := range []string{"number", "Number"} {
		value, ok := payload[field].(float64)
		if ok {
			return value
		}
	}
	require.Failf(t, "missing number field", "payload: %s", raw)
	return 0
}

func TestRestishRequesterFetchesCompleteJSON(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_RESTISH_CONFIG_DIR", t.TempDir())
	t.Setenv("MIDDLEMAN_RESTISH_CACHE_DIR", t.TempDir())
	longTitle := strings.Repeat("repo-", 1200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/repos", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprintf(w, `[{"name":%q}]`, longTitle)
		assert.NoError(err)
	}))
	t.Cleanup(server.Close)

	body, err := makeRestishRequest(context.Background(), cliConfig{timeout: 30 * time.Second}, http.MethodGet, server.URL+"/api/v1/repos", nil)

	require.NoError(err)
	assert.JSONEq(fmt.Sprintf(`[{"name":%q}]`, longTitle), string(body))
}

func TestRestishRequesterSetsJSONContentTypeForMutations(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_RESTISH_CONFIG_DIR", t.TempDir())
	t.Setenv("MIDDLEMAN_RESTISH_CACHE_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(http.MethodPost, r.Method)
		assert.Equal("application/json", r.Header.Get("Content-Type"))
		assert.Equal("application/json", r.Header.Get("Accept"))
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	body, err := makeRestishRequest(context.Background(), cliConfig{timeout: 30 * time.Second}, http.MethodPost, server.URL+"/api/v1/sync", nil)

	require.NoError(err)
	assert.Empty(body)
}

func TestRestishRequesterReturnsErrorResponseBody(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_RESTISH_CONFIG_DIR", t.TempDir())
	t.Setenv("MIDDLEMAN_RESTISH_CACHE_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, err := w.Write([]byte(`{"code":"not_found","details":{"repo":"acme/widget"}}`))
		assert.NoError(err)
	}))
	t.Cleanup(server.Close)

	body, err := makeRestishRequest(context.Background(), cliConfig{timeout: 30 * time.Second}, http.MethodGet, server.URL+"/api/v1/missing", nil)

	require.Error(err)
	assert.Contains(err.Error(), "404 Not Found")
	assert.Contains(err.Error(), `"code":"not_found"`)
	assert.JSONEq(`{"code":"not_found","details":{"repo":"acme/widget"}}`, string(body))
}

func TestWriteResponseFetchesYAMLBodyOnly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var yamlOut bytes.Buffer
	require.NoError(writeResponse(&yamlOut, "yaml", []byte(`{"issues":[{"number":46,"title":"agent output"}]}`)))
	assert.Contains(yamlOut.String(), "issues:")
	assert.Contains(yamlOut.String(), "number: 46")
	assert.NotContains(yamlOut.String(), "Content-Type")
	assert.NotContains(yamlOut.String(), `{"issues"`)
}

func TestWriteResponseFormatsJSONLines(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var arrayOut bytes.Buffer
	require.NoError(writeResponse(&arrayOut, "jsonl", []byte(`[{"number":46,"title":"agent output"},{"number":47,"title":"next"}]`)))
	lines := strings.Split(strings.TrimSpace(arrayOut.String()), "\n")
	require.Len(lines, 2)
	assert.JSONEq(`{"number":46,"title":"agent output"}`, lines[0])
	assert.JSONEq(`{"number":47,"title":"next"}`, lines[1])

	var objectOut bytes.Buffer
	require.NoError(writeResponse(&objectOut, "jsonl", []byte(`{"version":"dev"}`)))
	assert.JSONEq(`{"version":"dev"}`, strings.TrimSpace(objectOut.String()))
	assert.NotContains(objectOut.String(), "\n\n")
}
