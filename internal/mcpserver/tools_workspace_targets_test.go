package mcpserver

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceTargetUsesACPScopeOverHTTP(t *testing.T) {
	assert := assert.New(t)
	calls := 0
	s := newMCPTestServer(t, &fakeBackend{addWorkspaceTargetFn: func(_ context.Context, id string, request WorkspaceTargetRequest) (WorkspaceTarget, error) {
		calls++
		assert.Equal("ws-chat", id)
		assert.Equal(42, request.Item.Number)
		assert.Equal("https://github.com/acme/widget/pull/42", request.URL)
		return WorkspaceTarget{ID: 1, Type: "pr", Number: 42}, nil
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
	rejected, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_add_workspace_target", Arguments: map[string]any{
		"workspace_id": "ws-other", "item": itemRefInput{Type: "pr", Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 42}, "url": "https://github.com/acme/widget/pull/42",
	}})
	require.NoError(t, err)
	assert.True(rejected.IsError)
	assert.Equal(1, calls)
}

func TestWorkspaceTargetAcceptsTerminalWorkspaceID(t *testing.T) {
	called := false
	s := newMCPTestServer(t, &fakeBackend{addWorkspaceTargetFn: func(_ context.Context, id string, _ WorkspaceTargetRequest) (WorkspaceTarget, error) {
		called = true
		assert.Equal(t, "ws-terminal", id)
		return WorkspaceTarget{ID: 1}, nil
	}})
	client := connectMCPTestSession(t, s)
	response, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_add_workspace_target", Arguments: map[string]any{
		"workspace_id": "ws-terminal", "item": itemRefInput{Type: "pr", Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 42}, "url": "https://github.com/acme/widget/pull/42",
	}})
	require.NoError(t, err)
	assert.False(t, response.IsError)
	assert.True(t, called)
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
