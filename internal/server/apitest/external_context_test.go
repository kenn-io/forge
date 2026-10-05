package apitest

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/platform"
)

// The executable fixture consumes real stdin and records invocations, so a
// missing head check or an action pre-read changes observable output.
func TestExternalContextAdapterProcess(t *testing.T) { //nolint:paralleltest // subprocess entry point; owns the helper process's stdio and lifetime
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--context-fixture" {
		return
	}
	var request struct {
		Version     int            `json:"version"`
		Operation   string         `json:"operation"`
		ActionID    string         `json:"action_id"`
		Input       string         `json:"input,omitempty"`
		PullRequest map[string]any `json:"pull_request"`
	}
	if err := json.UnmarshalRead(os.Stdin, &request); err != nil {
		os.Exit(2)
	}
	data, err := json.Marshal(request)
	if err != nil {
		os.Exit(2)
	}
	log, err := os.OpenFile(os.Args[len(os.Args)-1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	if _, err := fmt.Fprintln(log, string(data)); err != nil {
		os.Exit(2)
	}
	if err := log.Close(); err != nil {
		os.Exit(2)
	}
	if err := json.MarshalWrite(os.Stdout, map[string]any{"card": map[string]any{
		"status": "success", "summary": request.Operation,
		"markdown": string(data), "result_head_sha": request.PullRequest["head_sha"],
		"actions": []map[string]any{{"id": "request-run", "label": "Run check"}, {"id": "comment", "label": "Comment", "input": map[string]any{}}},
	}}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestExternalContextAPI(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	executable, err := os.Executable()
	require.NoError(err)
	logPath := filepath.Join(dir, "invocations.jsonl")
	configPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(configPath, []byte(fmt.Sprintf(`data_dir = %q
allowed_hosts = ["forge.test"]
[[external_context]]
id = "checks"
name = "Private checks"
command = [%q, "-test.run=^TestExternalContextAdapterProcess$", "--", "--context-fixture", %q]
`, dir, executable, logPath)), 0o600))
	cfg, err := config.Load(configPath)
	require.NoError(err)
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, defaultTestRepos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := servertest.NewWithConfig(t, database, syncer, nil, nil, cfg, configPath, server.ServerOptions{})
	seedPRWithHeadSHA(t, database, "acme", "widget", 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	widgetID, ok := verifiedGitHubRepoIdentity("github.com", "acme", "widget").Key.ID()
	require.True(ok)

	client := setupTestClient(t, srv)
	sources, err := client.HTTP.ListExternalContextSourcesWithResponse(t.Context())
	require.NoError(err)
	require.NotNil(sources.JSON200)
	require.Len(sources.JSON200.Sources, 1)
	assert.Equal("Private checks", sources.JSON200.Sources[0].Name)
	assert.NotContains(string(sources.Body), executable)
	read, err := client.HTTP.GetPullExternalContextWithResponse(t.Context(), &generated.GetPullExternalContextRequestOptions{
		PathParams: &generated.GetPullExternalContextPath{Provider: "github", Owner: "acme", Name: "widget", Number: 1, SourceID: "checks"},
		Query:      &generated.GetPullExternalContextQuery{PlatformRepoID: &widgetID},
	})
	require.NoError(err)
	require.NotNil(read.JSON200)
	require.NotNil(read.JSON200.Card)
	require.NotNil(read.JSON200.Card.Markdown)
	assert.Contains(string(read.Body), `"id":"comment","label":"Comment","input":{}`, "an empty input object still marks the action")
	assert.JSONEq(fmt.Sprintf(`{"version":1,"operation":"read","action_id":"","pull_request":{"provider":"github","platform_host":"github.com","platform_repo_id":%d,"repo_path":"acme/widget","number":1,"url":"https://github.com/acme/widget/pull/1","state":"open","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_sha":""}}`, widgetID), *read.JSON200.Card.Markdown)
	for _, body := range []generated.ExternalContextActionRequest{
		{PlatformRepoID: &widgetID, HeadSha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{PlatformRepoID: new(widgetID + 1), HeadSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		response, err := client.HTTP.RunPullExternalContextActionWithResponse(t.Context(), &generated.RunPullExternalContextActionRequestOptions{
			PathParams: &generated.RunPullExternalContextActionPath{Provider: "github", Owner: "acme", Name: "widget", Number: 1, SourceID: "checks", ActionID: "request-run"}, Body: &body,
		})
		require.Error(err)
		assert.Equal(http.StatusConflict, response.StatusCode)
	}
	action, err := client.HTTP.RunPullExternalContextActionWithResponse(t.Context(), &generated.RunPullExternalContextActionRequestOptions{
		PathParams: &generated.RunPullExternalContextActionPath{Provider: "github", Owner: "acme", Name: "widget", Number: 1, SourceID: "checks", ActionID: "request-run"},
		Body:       &generated.ExternalContextActionRequest{PlatformRepoID: &widgetID, HeadSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Input: new("Looks good")},
	})
	require.NoError(err)
	require.Equal(http.StatusOK, action.StatusCode)
	oversized, err := client.HTTP.RunPullExternalContextActionWithResponse(t.Context(), &generated.RunPullExternalContextActionRequestOptions{
		PathParams: &generated.RunPullExternalContextActionPath{Provider: "github", Owner: "acme", Name: "widget", Number: 1, SourceID: "checks", ActionID: "request-run"},
		Body:       &generated.ExternalContextActionRequest{PlatformRepoID: &widgetID, HeadSha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Input: new(strings.Repeat("x", 16385))},
	})
	require.Error(err)
	assert.Equal(http.StatusUnprocessableEntity, oversized.StatusCode)
	invocations, err := os.ReadFile(logPath)
	require.NoError(err)
	lines := strings.Split(strings.TrimSpace(string(invocations)), "\n")
	require.Len(lines, 2, "rejected actions do not execute; accepted action has no pre-read")
	assert.Contains(lines[1], `"operation":"action"`)
	assert.Contains(lines[1], `"action_id":"request-run"`)
	assert.Contains(lines[1], `"input":"Looks good"`)
	assert.Contains(lines[1], `"head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`)

	{
		repoID, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{Platform: "gitlab", PlatformHost: "git.example.com", Key: platform.RepositoryIDKey(9101), Owner: "group/subgroup", Name: "project", RepoPath: "group/subgroup/project"})
		require.NoError(err)
		now := time.Now().UTC()
		_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 42, Number: 42, URL: "https://git.example.com/group/subgroup/project/-/merge_requests/42", Title: "Synthetic pull", Author: "user-a", State: db.MergeRequestStateClosed, PlatformHeadSHA: "cccccccccccccccccccccccccccccccccccccccc", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
		require.NoError(err)
		response, err := client.HTTP.GetPullExternalContextOnHostWithResponse(t.Context(), &generated.GetPullExternalContextOnHostRequestOptions{
			PathParams: &generated.GetPullExternalContextOnHostPath{PlatformHost: "git.example.com", Provider: "gitlab", Owner: "group/subgroup", Name: "project", Number: 42, SourceID: "checks"},
			Query:      &generated.GetPullExternalContextOnHostQuery{PlatformRepoID: new(int64(9101))},
		})
		require.NoError(err)
		require.NotNil(response.JSON200)
		require.NotNil(response.JSON200.Card)
		require.NotNil(response.JSON200.Card.Markdown)
		assert.Contains(*response.JSON200.Card.Markdown, `"platform_host":"git.example.com"`)
		assert.Contains(*response.JSON200.Card.Markdown, `"repo_path":"group/subgroup/project"`)
		assert.Contains(*response.JSON200.Card.Markdown, `"state":"closed"`)
	}
	// The generated settings DTO cannot express this forbidden member.
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "http://forge.test/api/v1/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	before, err := os.ReadFile(configPath)
	require.NoError(err)
	for _, body := range []string{
		`{"external_context":[{"id":"injected","command":["/bin/echo"]}]}`,
		`{"airplane_mode":true,"external_context":[]}`,
	} {
		response := request(body)
		assert.Equal(http.StatusUnprocessableEntity, response.Code, response.Body.String())
		assert.False(cfg.AirplaneMode)
		after, err := os.ReadFile(configPath)
		require.NoError(err)
		assert.Equal(before, after)
	}
	valid, err := client.HTTP.UpdateSettingsWithResponse(t.Context(), &generated.UpdateSettingsRequestOptions{Body: &generated.UpdateSettingsBody{AirplaneMode: new(true)}})
	require.NoError(err)
	require.Equal(http.StatusOK, valid.StatusCode)
	reloaded, err := config.Load(configPath)
	require.NoError(err)
	assert.True(reloaded.AirplaneMode)
	persisted, err := os.ReadFile(configPath)
	require.NoError(err)
	assert.Contains(string(persisted), "[[external_context]]")
	assert.Contains(string(persisted), logPath)
}
