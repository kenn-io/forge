package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
		Restish: func(context.Context, []string, *bytes.Buffer, *bytes.Buffer) error {
			return nil
		},
	})
	cmd.SetArgs([]string{"--help"})

	err := cmd.Execute()

	require.NoError(err)
	help := stdout.String()
	assert.Contains(help, "quickstart")
	assert.Contains(help, "api METHOD PATH")
	assert.Contains(help, "--output")
	assert.Empty(stderr.String())
}

func TestQuickstartFormatsJSONAndYAML(t *testing.T) {
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
}

func TestPullsCommandDelegatesToRestishWithAgentFriendlyDefaults(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got []string
	var stdout bytes.Buffer
	cmd := newRootCommand(commandDeps{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		Restish: func(_ context.Context, args []string, _ *bytes.Buffer, _ *bytes.Buffer) error {
			got = append([]string(nil), args...)
			return nil
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

	require.Len(got, 7)
	assert.Equal("--rsh-output-format", got[0])
	assert.Equal("yaml", got[1])
	assert.Equal("--rsh-no-cache", got[2])
	assert.Equal("--rsh-timeout", got[3])
	assert.Equal("30s", got[4])
	assert.Equal(http.MethodGet, got[5])
	requestURL, err := url.Parse(got[6])
	require.NoError(err)
	assert.Equal("http://middleman.test/api/v1/pulls", requestURL.Scheme+"://"+requestURL.Host+requestURL.Path)
	values := requestURL.Query()
	assert.Equal("open", values.Get("state"))
	assert.Equal("5", values.Get("limit"))
	assert.Equal("true", values.Get("starred"))
	assert.Empty(stdout.String())
}

func TestRawAPICommandBuildsMiddlemanAPIURLAndBodyArgs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got []string
	cmd := newRootCommand(commandDeps{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Restish: func(_ context.Context, args []string, _ *bytes.Buffer, _ *bytes.Buffer) error {
			got = append([]string(nil), args...)
			return nil
		},
	})
	cmd.SetArgs([]string{
		"--server", "http://middleman.test/",
		"api", "POST", "/pulls/gh/acme/widget/7/comments", "body: LGTM",
	})

	require.NoError(cmd.Execute())
	require.Len(got, 8)
	assert.Equal(http.MethodPost, got[5])
	assert.Equal("http://middleman.test/api/v1/pulls/gh/acme/widget/7/comments", got[6])
	assert.Equal("body: LGTM", got[7])
}

func TestRestishRunnerFetchesJSON(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMANCTL_RESTISH_CONFIG_DIR", t.TempDir())
	t.Setenv("MIDDLEMANCTL_RESTISH_CACHE_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/version", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"version":"test"}`))
		assert.NoError(err)
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := runRestish(context.Background(), []string{
		"--rsh-output-format", "json",
		"--rsh-no-cache",
		http.MethodGet,
		server.URL + "/api/v1/version",
	}, &stdout, &stderr)

	require.NoError(err, strings.TrimSpace(stderr.String()))
	assert.JSONEq(`{"version":"test"}`, stdout.String())
}
