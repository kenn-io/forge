package mcpserver

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"uuid"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func TestWorkspaceTargetUsesACPScopeOverHTTP(t *testing.T) {
	assert := assert.New(t)
	calls := 0
	s := newMCPTestServer(t, &fakeBackend{addWorkspaceTargetFn: func(_ context.Context, id string, request WorkspaceTargetRequest) (WorkspaceTarget, error) {
		calls++
		assert.Equal("ws-chat", id)
		assert.Equal(42, request.Item.Number)
		assert.Equal("https://github.com/acme/widget/pull/42", request.URL)
		return WorkspaceTarget{ID: 1, Type: "pr", Number: 42, Repository: new(testRepository())}, nil
	}})
	handler := s.HTTPHandler()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Kenn-Forge-Workspace-ID", "ws-chat")
		handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint.URL + "/mcp", DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	defer session.Close()
	response, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_add_workspace_target", Arguments: map[string]any{
		"item": itemRefInput{Type: "pr", Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 42}, "url": "https://github.com/acme/widget/pull/42",
	}})
	require.NoError(t, err)
	require.False(t, response.IsError)
	var result WorkspaceTarget
	require.NoError(t, json.Unmarshal([]byte(response.Content[0].(*mcp.TextContent).Text), &result))
	assert.Equal(int64(1), result.ID)
	assert.Equal(new(testRepository()), result.Repository)
	rejected, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_add_workspace_target", Arguments: map[string]any{
		"workspace_id": "ws-other", "item": itemRefInput{Type: "pr", Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 42}, "url": "https://github.com/acme/widget/pull/42",
	}})
	require.NoError(t, err)
	assert.True(rejected.IsError)
	assert.Equal(1, calls)
}

func TestWorkspaceTargetAcceptsTerminalWorkspaceID(t *testing.T) {
	repo := testRepository()
	repo.Provider = "bitbucket"
	repo.PlatformHost = "bitbucket.org"
	repo.Key = platform.RepositoryUUIDKey(uuid.MustParse("5f0c6a1e-2b7d-4c3a-9e8f-0a1b2c3d4e5f"))
	called := false
	s := newMCPTestServer(t, &fakeBackend{addWorkspaceTargetFn: func(_ context.Context, id string, request WorkspaceTargetRequest) (WorkspaceTarget, error) {
		called = true
		assert.Equal(t, "ws-terminal", id)
		assert.Equal(t, repo.Key, request.Item.RepoKey)
		return WorkspaceTarget{ID: 1, Repository: &repo}, nil
	}})
	client := connectMCPTestSession(t, s)
	response, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_add_workspace_target", Arguments: map[string]any{
		"workspace_id": "ws-terminal", "item": itemRefInput{Type: "pr", Provider: "bitbucket", PlatformHost: "bitbucket.org", BitbucketRepositoryUUID: "5f0c6a1e-2b7d-4c3a-9e8f-0a1b2c3d4e5f", Owner: "acme", Name: "widget", Number: 42}, "url": "https://bitbucket.org/acme/widget/pull-requests/42",
	}})
	require.NoError(t, err)
	require.False(t, response.IsError)
	assert.True(t, called)
	var result WorkspaceTarget
	require.NoError(t, json.Unmarshal([]byte(response.Content[0].(*mcp.TextContent).Text), &result))
	assert.Equal(t, &repo, result.Repository)
}

func TestWorkspaceTargetsListPreservesRepositoryKeys(t *testing.T) {
	cloudRepo := testRepository()
	cloudRepo.Provider = "bitbucket"
	cloudRepo.PlatformHost = "bitbucket.org"
	cloudRepo.Key = platform.RepositoryUUIDKey(uuid.MustParse("5f0c6a1e-2b7d-4c3a-9e8f-0a1b2c3d4e5f"))
	targets := []WorkspaceTarget{
		{ID: 1, Type: "pr", Number: 42, Repository: new(testRepository())},
		{ID: 2, Type: "pr", Number: 43, Repository: &cloudRepo},
	}
	s := newMCPTestServer(t, &fakeBackend{listWorkspaceTargetsFn: func(_ context.Context, id string) (WorkspaceTargets, error) {
		assert.Equal(t, "ws-terminal", id)
		return WorkspaceTargets{Targets: targets}, nil
	}})
	client := connectMCPTestSession(t, s)
	response, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_list_workspace_targets", Arguments: map[string]any{"workspace_id": "ws-terminal"}})
	require.NoError(t, err)
	require.False(t, response.IsError)
	encoded, err := json.Marshal(response.StructuredContent)
	require.NoError(t, err)
	var result WorkspaceTargets
	require.NoError(t, json.Unmarshal(encoded, &result))
	assert.Equal(t, targets, result.Targets)
}

func TestWorkspaceKataToolFollowsAvailabilityOverHTTP(t *testing.T) {
	var available atomic.Bool
	calls := 0
	s := newMCPTestServer(t, &fakeBackend{
		kataTargetsAvailableFn: available.Load,
		addWorkspaceTargetFn: func(_ context.Context, id string, in WorkspaceTargetRequest) (WorkspaceTarget, error) {
			calls++
			assert.Equal(t, "ws-task", id)
			require.NotNil(t, in.Kata)
			assert.Equal(t, "task-a", in.Kata.IssueUID)
			return WorkspaceTarget{ID: 1, Type: "kata", Kata: in.Kata}, nil
		},
	})
	endpoint := httptest.NewServer(s.HTTPHandler())
	defer endpoint.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint.URL + "/mcp", DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	defer session.Close()
	for _, enabled := range []bool{false, true, false} {
		available.Store(enabled)
		catalog, err := session.ListTools(t.Context(), nil)
		require.NoError(t, err)
		present := false
		for _, tool := range catalog.Tools {
			if tool.Name == "kenn_forge_add_workspace_kata_target" {
				present = true
			}
		}
		assert.Equal(t, enabled, present)
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_add_workspace_kata_target", Arguments: map[string]any{"workspace_id": "ws-task", "daemon_id": "primary", "project_uid": "project-a", "issue_uid": "task-a"}})
		if enabled {
			require.NoError(t, err)
			assert.False(t, result.IsError)
		} else {
			assert.True(t, err != nil || result.IsError)
		}
	}
	assert.Equal(t, 1, calls)
}
