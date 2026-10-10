package server

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/server/activityapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
)

func TestDevboxTargetsUseControllerMetadata(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert, require := assert.New(t), require.New(t)
	ctx := t.Context()
	workerDB, controllerDB := dbtest.Open(t), dbtest.Open(t)
	identity := db.GitHubRepoIdentity("github.com", "example-org", "project")
	identity.Key = platform.RepositoryIDKey(1001)
	workerRepoID, err := reposeed.Seed(ctx, workerDB, identity)
	require.NoError(err)
	repoID, err := reposeed.Seed(ctx, controllerDB, identity)
	require.NoError(err)
	other := db.RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.example.com", Owner: "example-org", Name: "tools"}
	other.Key = platform.RepositoryIDKey(1002)
	otherID, err := reposeed.Seed(ctx, controllerDB, other)
	require.NoError(err)
	require.NoError(workerDB.InsertWorkspace(ctx, &db.Workspace{
		ID: "work-a", Platform: "github", PlatformHost: "github.com", RepoOwner: "example-org", RepoName: "project",
		ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 1, WorktreePath: t.TempDir(), Status: "ready",
	}))
	now := time.Now().UTC()
	_, err = controllerDB.UpsertIssue(ctx, &db.Issue{RepoID: repoID, PlatformID: 1, Number: 1, Title: "Source issue", State: "open", URL: "https://github.com/example-org/project/issues/1", CreatedAt: now, UpdatedAt: now})
	require.NoError(err)
	_, err = controllerDB.UpsertMergeRequest(ctx, &db.MergeRequest{RepoID: otherID, PlatformID: 7, Number: 7, Title: "Related change", State: "merged", URL: "https://gitlab.example.com/example-org/tools/-/merge_requests/7", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(err)
	workerAPI := workspaceapi.New(workspaceapi.Deps{DB: workerDB, ExecutionWorker: config.ExecutionWorker{Enabled: true, GitHubUserID: 1234}})
	t.Cleanup(func() { require.NoError(workerAPI.Shutdown(context.Background())) })
	workerMux := http.NewServeMux()
	api := humago.NewWithPrefix(workerMux, "/api/v1", activityapi.WithRepositoryKeyWireSchemas(huma.DefaultConfig("worker", "1")))
	workerAPI.RegisterExecution(api)
	workerAPI.RegisterWorker(api)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal("Bearer fixture-bearer", r.Header.Get("Authorization")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		workerMux.ServeHTTP(w, r)
	}))
	t.Cleanup(worker.Close)
	directory := t.TempDir()
	raw, err := json.Marshal([]any{map[string]any{"id": "compute-a", "profile": devbox.Profile{Assignment: devbox.Assignment{URL: worker.URL}, Token: "fixture-bearer"}}})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), raw, 0o600))
	connections, err := devbox.OpenConnections(directory)
	require.NoError(err)
	t.Cleanup(connections.Close)
	controllerAPI := workspaceapi.New(workspaceapi.Deps{DB: controllerDB})
	t.Cleanup(func() { require.NoError(controllerAPI.Shutdown(context.Background())) })
	controller := &Server{db: controllerDB, workspaceAPI: controllerAPI, options: ServerOptions{Devboxes: connections}}
	mux := http.NewServeMux()
	api = humago.New(mux, activityapi.WithRepositoryKeyWireSchemas(huma.DefaultConfig("controller", "1")))
	controllerAPI.RegisterExecution(api)
	controller.registerDevboxAPI(api)
	request := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, "/devboxes/compute-a/workspaces/work-a/targets", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	source := `{"repository":{"provider":"github","platform_host":"github.com","platform_repo_id":1001},"type":"issue","number":1,"hidden":false}`
	related := `{"repository":{"provider":"gitlab","platform_host":"gitlab.example.com","platform_repo_id":1002},"type":"pr","number":7,"hidden":false}`
	read := func() workspaceapi.WorkspaceTargetsResponse {
		response := request(http.MethodGet, "")
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		var targets workspaceapi.WorkspaceTargetsResponse
		require.NoError(json.Unmarshal(response.Body.Bytes(), &targets))
		return targets
	}
	initial := read()
	require.Len(initial.Targets, 1)
	assert.Equal("Source issue", initial.Targets[0].Title)
	assert.False(initial.Targets[0].Unavailable)
	for _, body := range []string{source, related} {
		response := request(http.MethodPut, body)
		require.Equal(http.StatusNoContent, response.Code, response.Body.String())
	}
	targets := read()
	require.Len(targets.Targets, 2)
	byTitle := map[string]workspaceapi.WorkspaceTarget{}
	for _, target := range targets.Targets {
		byTitle[target.Title] = target
		assert.False(target.Unavailable)
	}
	assert.Equal("open", byTitle["Source issue"].State)
	assert.Equal("owner", byTitle["Source issue"].Source)
	assert.Equal("merged", byTitle["Related change"].State)
	assert.Equal("https://gitlab.example.com/example-org/tools/-/merge_requests/7", byTitle["Related change"].URL)
	require.NotNil(byTitle["Related change"].Repo)
	assert.Equal(other.Key, byTitle["Related change"].Repo.Key)
	response := request(http.MethodPut, strings.Replace(source, `"hidden":false`, `"hidden":true`, 1))
	require.Equal(http.StatusNoContent, response.Code, response.Body.String())
	assert.Len(read().Targets, 1)
	response = request(http.MethodPut, source)
	require.Equal(http.StatusNoContent, response.Code, response.Body.String())
	assert.Len(read().Targets, 2)
	rows, err := workerDB.ListWorkspaceTargets(ctx, "work-a")
	require.NoError(err)
	assert.Len(rows, 2)
	// Provider items remain owned by the controller throughout visits and removals.
	issue, err := workerDB.GetIssueByRepoIDAndNumber(ctx, workerRepoID, 1)
	require.NoError(err)
	assert.Nil(issue)
	workerOther, err := workerDB.GetRepositoryByProviderID(ctx, platform.RepositoryIdentity{Provider: "gitlab", PlatformHost: "gitlab.example.com", Key: other.Key})
	require.NoError(err)
	require.NotNil(workerOther)
	pull, err := workerDB.GetMergeRequestByRepoIDAndNumber(ctx, workerOther.Repository.ID, 7)
	require.NoError(err)
	assert.Nil(pull)
	_, err = controllerDB.DeactivateRepository(ctx, platform.RepositoryIdentity{Provider: "gitlab", PlatformHost: "gitlab.example.com", Key: other.Key})
	require.NoError(err)
	unavailable := read()
	require.Len(unavailable.Targets, 2)
	for _, target := range unavailable.Targets {
		if target.Type == "pr" {
			assert.True(target.Unavailable)
			assert.Equal("https://gitlab.example.com/example-org/tools/-/merge_requests/7", target.URL)
		}
	}
	response = request(http.MethodPut, strings.Replace(related, `"hidden":false`, `"hidden":true`, 1))
	require.Equal(http.StatusNoContent, response.Code, response.Body.String())
	assert.Len(read().Targets, 1)
}
