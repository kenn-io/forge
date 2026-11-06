package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	configpkg "go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.yaml.in/yaml/v3"
)

func TestDefaultServerComesFromForgeConfig(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_HOME", t.TempDir())
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
		Request: func(_ context.Context, _ cliConfig, _ string, requestURL string, _ []string) ([]byte, error) {
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
		Request: func(context.Context, cliConfig, string, string, []string) ([]byte, error) {
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

func TestRepositoryCommandsPreserveHTTPAndOutputContracts(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		for _, format := range []string{"json", "yaml", "jsonl"} {
			t.Run(fmt.Sprintf("%d/%s", status, format), func(t *testing.T) {
				assert := assert.New(t)
				var requests []string
				var requestsMu sync.Mutex
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestsMu.Lock()
					requests = append(requests, r.Method+" "+r.URL.RequestURI())
					requestsMu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, err := w.Write([]byte(`[{"name":"acme/widget","open_pulls":3}]`))
					assert.NoError(err)
				}))
				t.Cleanup(server.Close)
				var outputs, failures []string
				for _, args := range [][]string{{"repos", "summaries"}, {"repo-summaries"}, {"repos"}} {
					var stdout, stderr bytes.Buffer
					cmd := newCommand(commandDeps{Stdout: &stdout, Stderr: &stderr})
					cmd.SilenceErrors = true
					cmd.SilenceUsage = true
					cmd.SetArgs(append([]string{"--server", server.URL, "--output", format, "--timeout", "1m"}, args...))
					err := cmd.Execute()
					if status == http.StatusOK {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
					outputs = append(outputs, stdout.String())
					failures = append(failures, fmt.Sprint(err))
					if args[0] == "repo-summaries" {
						assert.Contains(stderr.String(), `deprecated, use "kenn-forge repos summaries" instead`)
					} else {
						assert.Empty(stderr.String())
					}
				}
				requestsMu.Lock()
				assert.Equal([]string{"GET /api/v1/repos/summary", "GET /api/v1/repos/summary", "GET /api/v1/repos"}, requests)
				requestsMu.Unlock()
				assert.NotEmpty(outputs[0])
				if format == "jsonl" {
					for _, output := range outputs[:2] {
						assert.JSONEq(`{"name":"acme/widget","open_pulls":3}`, output)
						assert.Len(strings.Split(strings.TrimSpace(output), "\n"), 1)
					}
				} else {
					assert.Equal(outputs[0], outputs[1])
				}
				assert.Equal(failures[0], failures[1])
				assert.NotContains(outputs[1], "deprecated")
			})
		}
	}
}

func TestLegacyRepositorySummariesWarnsOnHelpAndInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		{"repo-summaries", "--help"},
		{"help", "repo-summaries"},
		{"repo-summaries", "--unknown"},
		{"repo-summaries", "unexpected"},
		{"repo-summaries", "--output", "invalid"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			assert := assert.New(t)
			var stdout, stderr bytes.Buffer
			cmd := newCommand(commandDeps{Stdout: &stdout, Stderr: &stderr})
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs(args)
			err := cmd.Execute()
			if args[0] == "help" || args[1] == "--help" {
				require.NoError(t, err)
				assert.Contains(stdout.String(), "Usage:")
			} else {
				require.Error(t, err)
				assert.Empty(stdout.String())
			}
			assert.Equal(1, strings.Count(stderr.String(), "deprecated"))
			assert.Contains(stderr.String(), "kenn-forge repos summaries")
		})
	}
}

func TestRepositorySummariesDiscovery(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"__complete", ""},
		{"__complete", "repo-"},
		{"repos", "--help"},
		{"__complete", "repos", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			assert := assert.New(t)
			var stdout, stderr bytes.Buffer
			cmd := newCommand(commandDeps{Stdout: &stdout, Stderr: &stderr})
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			assert.NotContains(stdout.String(), "repo-summaries")
			assert.NotContains(stderr.String(), "deprecated")
			if slices.Contains(args, "repos") {
				assert.Contains(stdout.String(), "summaries")
			}
		})
	}
}

func TestQuickstartFormatsStructuredOutput(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var jsonOut bytes.Buffer
	cmd := newCommand(commandDeps{
		Stdout: &jsonOut,
		Stderr: &bytes.Buffer{},
	})
	cmd.SetArgs([]string{"--server", "http://forge.test", "quickstart"})

	require.NoError(cmd.Execute())

	var payload map[string]any
	require.NoError(json.Unmarshal(jsonOut.Bytes(), &payload))
	assert.Equal("http://forge.test/api/v1", payload["api_base_url"])
	assert.Contains(jsonOut.String(), "kenn-forge api GET /pulls")
	assert.Contains(jsonOut.String(), "kenn-forge api GET /version")
	assert.Contains(jsonOut.String(), "kenn-forge mcp quickstart")
	assert.Contains(jsonOut.String(), "Print up to 20 open PR summaries; add --full for API records")
	assert.Contains(jsonOut.String(), "Print up to 50 issue summaries as JSON lines; add --full for API records")

	var yamlOut bytes.Buffer
	cmd = newCommand(commandDeps{
		Stdout: &yamlOut,
		Stderr: &bytes.Buffer{},
	})
	cmd.SetArgs([]string{"--server", "http://forge.test", "--output", "yaml", "quickstart"})

	require.NoError(cmd.Execute())
	assert.Contains(yamlOut.String(), "api_base_url: http://forge.test/api/v1")
	assert.Contains(yamlOut.String(), "kenn-forge api GET /sync/status")

	var jsonlOut bytes.Buffer
	cmd = newCommand(commandDeps{
		Stdout: &jsonlOut,
		Stderr: &bytes.Buffer{},
	})
	cmd.SetArgs([]string{"--server", "http://forge.test", "--output", "jsonl", "quickstart"})

	require.NoError(cmd.Execute())
	lines := strings.Split(strings.TrimSpace(jsonlOut.String()), "\n")
	require.Len(lines, 1)
	assert.Contains(lines[0], `"api_base_url":"http://forge.test/api/v1"`)
	assert.Contains(lines[0], `"jsonl"`)
}

func TestPullsCommandDelegatesToRequestWithAgentFriendlyDefaults(t *testing.T) {
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
		Request: func(_ context.Context, cfg cliConfig, method, requestURL string, bodyArgs []string) ([]byte, error) {
			got.cfg = cfg
			got.method = method
			got.url = requestURL
			got.bodyArgs = append([]string(nil), bodyArgs...)
			return []byte(`[{"Number":7,"Title":"ready"}]`), nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://forge.test",
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
	assert.Equal("http://forge.test/api/v1/pulls", requestURL.Scheme+"://"+requestURL.Host+requestURL.Path)
	values := requestURL.Query()
	assert.Equal("open", values.Get("state"))
	assert.Equal("6", values.Get("limit"))
	assert.Equal("true", values.Get("starred"))
	assert.Empty(got.bodyArgs)
	assert.Contains(stdout.String(), "number: 7")
	assert.NotContains(stdout.String(), "Content-Type")
}

func TestRawAPICommandBuildsForgeAPIURLAndBodyArgs(t *testing.T) {
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
		Request: func(_ context.Context, _ cliConfig, method, requestURL string, bodyArgs []string) ([]byte, error) {
			got.method = method
			got.url = requestURL
			got.bodyArgs = append([]string(nil), bodyArgs...)
			return nil, nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://forge.test/",
		"api", "POST", "/pulls/gh/acme/widget/7/comments", "body: LGTM",
	})

	require.NoError(cmd.Execute())
	assert.Equal(http.MethodPost, got.method)
	assert.Equal("http://forge.test/api/v1/pulls/gh/acme/widget/7/comments", got.url)
	assert.Equal([]string{"body: LGTM"}, got.bodyArgs)
}

func TestRawAPICommandRejectsAbsoluteURLs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	called := false
	cmd := newCommand(commandDeps{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Request: func(context.Context, cliConfig, string, string, []string) ([]byte, error) {
			called = true
			return nil, nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://forge.test/",
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
		Request: func(context.Context, cliConfig, string, string, []string) ([]byte, error) {
			return []byte(`{"code":"not_found","details":{"repo":"acme/widget"}}`),
				errors.New("kenn-forge API returned 404 Not Found")
		},
	})
	cmd.SetArgs([]string{"--server", "http://forge.test/", "api", "GET", "/missing"})

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
				Request: func(context.Context, cliConfig, string, string, []string) ([]byte, error) {
					called = true
					return nil, nil
				},
			})
			cmd.SetArgs([]string{"--server", "http://forge.test/", "api", "GET", rawPath})

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
		Request: func(_ context.Context, _ cliConfig, method, requestURL string, _ []string) ([]byte, error) {
			got.method = method
			got.url = requestURL
			return nil, nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://forge.test/",
		"api", "GET", "/host/gitlab.example.com/pulls/gl/Group%2FSubGroup/project/7",
	})

	require.NoError(cmd.Execute())
	assert.Equal(http.MethodGet, got.method)
	assert.Equal(
		"http://forge.test/api/v1/host/gitlab.example.com/pulls/gl/Group%2FSubGroup/project/7",
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
		Request: func(_ context.Context, _ cliConfig, method, requestURL string, _ []string) ([]byte, error) {
			got.method = method
			got.url = requestURL
			return nil, nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://forge.test/",
		"pulls", "get", "--host", "gitlab.example.com", "gl", "Group/SubGroup", "project", "7",
	})

	require.NoError(cmd.Execute())
	assert.Equal(http.MethodGet, got.method)
	assert.Equal(
		"http://forge.test/api/v1/host/gitlab.example.com/pulls/gl/Group%2FSubGroup/project/7",
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
		APICommandFactory: func(APIRequest) *cobra.Command {
			return &cobra.Command{
				Use:  "api METHOD PATH",
				Args: cobra.ExactArgs(2),
				RunE: func(cmd *cobra.Command, args []string) error {
					return errors.New("raw API relay should not handle api list")
				},
			}
		},
		Request: func(_ context.Context, _ cliConfig, method, requestURL string, bodyArgs []string) ([]byte, error) {
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
	cmd.SetArgs([]string{"--server", "http://forge.test", "--output", "jsonl", "api", "list"})

	require.NoError(cmd.Execute())

	assert.Equal(http.MethodGet, got.method)
	assert.Equal("http://forge.test/api/v1/openapi.json", got.url)
	assert.Empty(got.bodyArgs)
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	require.Len(lines, 2)
	assert.JSONEq(`{"method":"GET","path":"/pulls","operation_id":"list-pulls","summary":"List pulls","query_params":["limit","repo"]}`, lines[0])
	assert.JSONEq(`{"method":"GET","path":"/pulls/{provider}/{owner}/{name}/{number}","operation_id":"get-pull","summary":"Get pull"}`, lines[1])
	assert.NotContains(lines[1], "path_params")
}

func TestForgectlCommandsUseRealAPIAndSQLite(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts := setupForgectlE2E(t)

	pullsOut := runForge(t, ts.URL, "--output", "jsonl", "pulls", "--limit", "2")
	pullLines := strings.Split(strings.TrimSpace(pullsOut), "\n")
	require.Len(pullLines, 2)
	assert.NotZero(jsonNumberField(t, pullLines[0]))
	assert.NotZero(jsonNumberField(t, pullLines[1]))

	issuesOut := runForge(t, ts.URL, "--output", "jsonl", "issues", "--limit", "2")
	issueLines := strings.Split(strings.TrimSpace(issuesOut), "\n")
	require.Len(issueLines, 2)
	assert.NotZero(jsonNumberField(t, issueLines[0]))
	assert.NotZero(jsonNumberField(t, issueLines[1]))

	for _, lines := range [][]string{pullLines, issueLines} {
		for _, line := range lines {
			var row map[string]any
			require.NoError(json.Unmarshal([]byte(line), &row))
			repo, ok := row["repo"].(map[string]any)
			require.True(ok)
			assert.Equal("github", repo["provider"])
			assert.NotEmpty(row["title"])
			assert.NotEmpty(row["url"])
		}
	}

	versionOut := runForge(t, ts.URL, "api", "GET", "/version")
	assert.Contains(versionOut, `"version"`)

	starOut := runForge(
		t, ts.URL,
		"api", "PUT", "/starred",
		"item_type: pr, provider: github, platform_host: github.com, owner: acme, name: widgets, number: 1",
	)
	assert.Empty(starOut)
	starredOut := runForge(t, ts.URL, "--output", "jsonl", "pulls", "--starred", "--full")
	starredLines := strings.Split(strings.TrimSpace(starredOut), "\n")
	require.Len(starredLines, 1)
	assert.InDelta(float64(1), jsonNumberField(t, starredLines[0]), 0)
	assert.True(jsonBoolField(t, starredLines[0], "starred", "Starred"))

	apiListOut := runForge(t, ts.URL, "--output", "jsonl", "api", "list")
	assert.Contains(apiListOut, `"method":"GET"`)
	assert.Contains(apiListOut, `"path":"/pulls"`)

	syncOut := runForge(t, ts.URL, "sync")
	assert.Empty(syncOut)
}

func setupForgectlE2E(t *testing.T) *httptest.Server {
	t.Helper()
	database := dbtest.Open(t)
	_, err := testutil.SeedFixtures(t.Context(), database)
	require.NoError(t, err)

	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		require.NoError(t, srv.Shutdown(ctx))
	})

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func runForge(t *testing.T, serverURL string, args ...string) string {
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

func jsonBoolField(t *testing.T, raw string, fields ...string) bool {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	for _, field := range fields {
		value, ok := payload[field].(bool)
		if ok {
			return value
		}
	}
	require.Failf(t, "missing boolean field", "payload: %s", raw)
	return false
}

func TestAPIRequesterFetchesCompleteJSON(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	longTitle := strings.Repeat("repo-", 1200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/repos", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprintf(w, `[{"name":%q}]`, longTitle)
		assert.NoError(err)
	}))
	t.Cleanup(server.Close)

	body, err := makeAPIRequest(t.Context(), cliConfig{timeout: 30 * time.Second}, http.MethodGet, server.URL+"/api/v1/repos", nil)

	require.NoError(err)
	assert.JSONEq(fmt.Sprintf(`[{"name":%q}]`, longTitle), string(body))
}

func TestAPIRequesterSetsJSONContentTypeForMutations(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(http.MethodPost, r.Method)
		assert.Equal("application/json", r.Header.Get("Content-Type"))
		assert.Equal("application/json", r.Header.Get("Accept"))
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	body, err := makeAPIRequest(t.Context(), cliConfig{timeout: 30 * time.Second}, http.MethodPost, server.URL+"/api/v1/sync", nil)

	require.NoError(err)
	assert.Empty(body)
}

func TestAPIRequesterEncodesShorthandBodyArgs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(http.MethodPost, r.Method)
		assert.Equal("application/json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		assert.NoError(err)
		assert.JSONEq(`{"body":"LGTM"}`, string(body))
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	body, err := makeAPIRequest(
		t.Context(),
		cliConfig{timeout: 30 * time.Second},
		http.MethodPost,
		server.URL+"/api/v1/pulls/gh/acme/widget/7/comments",
		[]string{"body: LGTM"},
	)

	require.NoError(err)
	assert.Empty(body)
}

func TestAPIRequesterReturnsErrorResponseBody(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, err := w.Write([]byte(`{"code":"not_found","details":{"repo":"acme/widget"}}`))
		assert.NoError(err)
	}))
	t.Cleanup(server.Close)

	body, err := makeAPIRequest(t.Context(), cliConfig{timeout: 30 * time.Second}, http.MethodGet, server.URL+"/api/v1/missing", nil)

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

func TestPullsAndIssuesPrintSummariesWithLimitHint(t *testing.T) {
	for _, resource := range []string{"pulls", "issues"} {
		t.Run(resource, func(t *testing.T) {
			noun := "issues"
			if resource == "pulls" {
				noun = "pull requests"
			}
			for _, tc := range []struct {
				name   string
				args   []string
				limit  string
				count  int
				hint   string
				format string
			}{
				{name: "default", limit: "51", count: 50, hint: "50"},
				{name: "offset", args: []string{"--offset", "50"}, limit: "51", count: 10},
				{name: "negative offset", args: []string{"--limit", "1", "--offset", "-1"}, limit: "2", count: 1, hint: "1"},
				{name: "full negative offset", args: []string{"--full", "--limit", "1", "--offset", "-1"}, limit: "2", count: 1, hint: "1"},
				{name: "limit", args: []string{"--limit", "5", "--offset", "10"}, limit: "6", count: 5, hint: "15"},
				{name: "unlimited", args: []string{"--limit", "0"}, limit: "0", count: 60},
				{name: "full", args: []string{"--full"}, limit: "51", count: 50, hint: "50"},
				{name: "full yaml", args: []string{"--full", "-o", "yaml"}, limit: "51", count: 50, hint: "50", format: "yaml"},
				{name: "full jsonl", args: []string{"--full", "-o", "jsonl"}, limit: "51", count: 50, hint: "50", format: "jsonl"},
				{name: "full offset", args: []string{"--full", "--offset", "50"}, limit: "51", count: 10},
				{name: "full unlimited", args: []string{"--full", "--limit", "0"}, limit: "0", count: 60},
				{name: "yaml", args: []string{"-o", "yaml"}, limit: "51", count: 50, hint: "50", format: "yaml"},
				{name: "jsonl", args: []string{"-o", "jsonl"}, limit: "51", count: 50, hint: "50", format: "jsonl"},
				{name: "empty", args: []string{"--offset", "60"}, limit: "51"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					assert := assert.New(t)
					var stdout, stderr bytes.Buffer
					var response []byte
					cmd := newCommand(commandDeps{
						Stdout: &stdout, Stderr: &stderr,
						Request: func(_ context.Context, _ cliConfig, method, requestURL string, _ []string) ([]byte, error) {
							u, err := url.Parse(requestURL)
							require.NoError(t, err)
							assert.Equal(http.MethodGet, method)
							assert.Equal("/api/v1/"+resource, u.Path)
							assert.Equal(tc.limit, u.Query().Get("limit"))
							assert.False(u.Query().Has("full"))
							offset, _ := strconv.Atoi(u.Query().Get("offset"))
							offset = max(offset, 0)
							limit, _ := strconv.Atoi(u.Query().Get("limit"))
							end := 60
							if limit > 0 {
								end = min(end, offset+limit)
							}
							rows := make([]map[string]any, 0)
							for i := offset; i < end; i++ {
								rows = append(rows, map[string]any{
									"Number": i + 1, "Title": "A compact fixture title", "Author": "user-a", "State": "open",
									"LastActivityAt": "2026-11-06T12:00:00Z", "URL": "https://example.org/acme/widgets/pull/1",
									"IsDraft": true, "CIStatus": "success", "ReviewDecision": "approved",
									"Body": strings.Repeat("x", 5000), "CIChecksJSON": "[{\"name\":\"build\"}]",
									"repo": map[string]any{"provider": "github", "platform_host": "github.com", "owner": "acme", "name": "widgets"},
								})
							}
							response, err = json.Marshal(rows)
							require.NoError(t, err)
							return response, nil
						},
					})
					cmd.SetArgs(append([]string{"--server", "http://forge.test", resource}, tc.args...))
					require.NoError(t, cmd.Execute())
					if tc.hint != "" {
						assert.Equal("Hint: more "+noun+" match; use --offset "+tc.hint+" or raise --limit\n", stderr.String())
					} else {
						assert.Empty(stderr.String())
					}

					var rows []map[string]any
					switch tc.format {
					case "yaml":
						require.NoError(t, yaml.Unmarshal(stdout.Bytes(), &rows))
					case "jsonl":
						for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
							var row map[string]any
							require.NoError(t, json.Unmarshal([]byte(line), &row))
							rows = append(rows, row)
						}
					default:
						require.NoError(t, json.Unmarshal(stdout.Bytes(), &rows))
					}
					require.Len(t, rows, tc.count)
					if strings.HasPrefix(tc.name, "full") {
						for _, row := range rows {
							assert.Len(row["Body"], 5000)
							assert.Equal("[{\"name\":\"build\"}]", row["CIChecksJSON"])
						}
						if tc.name == "full offset" || tc.name == "full unlimited" {
							assert.Equal(string(response), stdout.String())
						}
						return
					}
					if tc.name == "default" {
						assert.Less(stdout.Len(), 25000)
					}
					if tc.name == "empty" {
						assert.Equal("[]\n", stdout.String())
					}
					for _, row := range rows {
						expected := []string{"number", "repo", "title", "author", "state", "last_activity_at", "url"}
						if resource == "pulls" {
							expected = append(expected, "is_draft", "ci_status", "review_decision")
						}
						assert.ElementsMatch(expected, slices.Collect(maps.Keys(row)))
						assert.Equal("A compact fixture title", row["title"])
						assert.Equal("2026-11-06T12:00:00Z", row["last_activity_at"])
						assert.Equal("github", row["repo"].(map[string]any)["provider"])
					}
				})
			}
		})
	}
}

func TestPullsHelpNamesFullAndGet(t *testing.T) {
	var out bytes.Buffer
	cmd := newCommand(commandDeps{Stdout: &out, Stderr: &out})
	cmd.SetArgs([]string{"pulls", "--help"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "--full")
	assert.Contains(t, out.String(), "pulls get")
	assert.Contains(t, out.String(), "--host PLATFORM_HOST")
}
