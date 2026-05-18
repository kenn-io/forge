package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootHelpPointsAgentsToQuickstartAndAPI(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newRootCommand(commandDeps{
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
	cmd := newRootCommand(commandDeps{
		Stdout: &jsonOut,
		Stderr: &bytes.Buffer{},
	})
	cmd.SetArgs([]string{"--server", "http://middleman.test", "quickstart"})

	require.NoError(cmd.Execute())

	var payload map[string]any
	require.NoError(json.Unmarshal(jsonOut.Bytes(), &payload))
	assert.Equal("http://middleman.test/api/v1", payload["api_base_url"])
	assert.Contains(jsonOut.String(), "middlemanctl api GET /pulls")

	var yamlOut bytes.Buffer
	cmd = newRootCommand(commandDeps{
		Stdout: &yamlOut,
		Stderr: &bytes.Buffer{},
	})
	cmd.SetArgs([]string{"--server", "http://middleman.test", "--output", "yaml", "quickstart"})

	require.NoError(cmd.Execute())
	assert.Contains(yamlOut.String(), "api_base_url: http://middleman.test/api/v1")
	assert.Contains(yamlOut.String(), "middlemanctl api GET /sync/status")

	var jsonlOut bytes.Buffer
	cmd = newRootCommand(commandDeps{
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
	cmd := newRootCommand(commandDeps{
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
	cmd := newRootCommand(commandDeps{
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

func TestAPIListCommandDiscoversOpenAPIOperations(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got struct {
		method   string
		url      string
		bodyArgs []string
	}
	var stdout bytes.Buffer
	cmd := newRootCommand(commandDeps{
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

func TestRestishRequesterFetchesCompleteJSON(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMANCTL_RESTISH_CONFIG_DIR", t.TempDir())
	t.Setenv("MIDDLEMANCTL_RESTISH_CACHE_DIR", t.TempDir())
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
