package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/testutil/reposeed"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

// mcpIdentitySession serves Forge's MCP tools from a real server backend
// with one seeded GitHub PR, acme/widgets#7, and returns the repository's
// provider ID.
func mcpIdentitySession(t *testing.T) (*mcp.ClientSession, int64) {
	t.Helper()
	srv, database, _ := setupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widgets", 7)
	toolServer, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, toolServer.Close()) })
	httpServer := httptest.NewServer(toolServer.HTTPHandler())
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "identity-test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, session.Close()) })
	return session, reposeed.SyntheticID(db.GitHubRepoIdentity("github.com", "acme", "widgets"))
}

func TestMCPItemContextResolvesIncompleteRepositoryIdentity(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	session, repoID := mcpIdentitySession(t)
	tests := []struct {
		name string
		item map[string]any
	}{
		{
			name: "route only",
			item: map[string]any{"type": "pr", "provider": "github", "owner": "acme", "name": "widgets", "number": 7},
		},
		{
			name: "provider ID without host",
			item: map[string]any{
				"type": "pr", "provider": "github", "platform_repo_id": repoID,
				"owner": "acme", "name": "widgets", "number": 7,
			},
		},
		{
			name: "provider ID with a stale route",
			item: map[string]any{
				"type": "pr", "provider": "github", "platform_repo_id": repoID,
				"owner": "acme", "name": "renamed-away", "number": 7,
			},
		},
		{
			name: "nested repository from a listing",
			item: map[string]any{
				"type": "pr", "number": 7,
				"repository": map[string]any{
					"provider": "github", "platform_host": "github.com", "platform_repo_id": repoID,
					"repo_path": "acme/widgets", "owner": "acme", "name": "widgets",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "kenn_forge_get_item_context", Arguments: map[string]any{"item": tt.item},
			})

			require.NoError(t, err)
			require.False(t, result.IsError, "%v", result.Content)
			encoded, err := json.Marshal(result.StructuredContent)
			require.NoError(t, err)
			var out struct {
				Item struct {
					PlatformHost   string `json:"platform_host"`
					PlatformRepoID int64  `json:"platform_repo_id"`
					Name           string `json:"name"`
					Number         int    `json:"number"`
					Title          string `json:"title"`
				} `json:"item"`
			}
			require.NoError(t, json.Unmarshal(encoded, &out))
			assert.Equal(t, "github.com", out.Item.PlatformHost)
			assert.Equal(t, repoID, out.Item.PlatformRepoID)
			assert.Equal(t, "widgets", out.Item.Name)
			assert.Equal(t, 7, out.Item.Number)
			assert.Equal(t, "Test PR #7", out.Item.Title)
		})
	}
}

func TestMCPItemContextRejectsUnusableIdentityWithoutStructuredOutput(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	session, repoID := mcpIdentitySession(t)
	tests := []struct {
		name      string
		item      map[string]any
		wantKind  string
		wantField string
	}{
		{
			name: "nested repository conflicting with the flat fields",
			item: map[string]any{
				"type": "pr", "number": 7, "provider": "github", "platform_repo_id": repoID + 1,
				"repository": map[string]any{
					"provider": "github", "platform_repo_id": repoID, "owner": "acme", "name": "widgets",
				},
			},
			wantKind:  "invalid_request",
			wantField: "item.platform_repo_id",
		},
		{
			name:     "untracked route",
			item:     map[string]any{"type": "pr", "provider": "github", "owner": "acme", "name": "missing", "number": 7},
			wantKind: "not_found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "kenn_forge_get_item_context", Arguments: map[string]any{"item": tt.item},
			})

			require.NoError(t, err)
			require.True(t, result.IsError)
			assert.Nil(t, result.StructuredContent)
			text, ok := result.Content[0].(*mcp.TextContent)
			require.True(t, ok)
			var payload struct {
				Error struct {
					Kind    string         `json:"kind"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))
			assert.Equal(t, tt.wantKind, payload.Error.Kind)
			if tt.wantField != "" {
				assert.Equal(t, tt.wantField, payload.Error.Details["field"])
			}
		})
	}
}

func TestMCPStackAndSearchAcceptIncompleteRepositoryIdentity(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	session, repoID := mcpIdentitySession(t)

	stack, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_get_stack_context",
		Arguments: map[string]any{"item": map[string]any{
			"type": "pr", "provider": "github", "platform_repo_id": repoID,
			"owner": "acme", "name": "widgets", "number": 7,
		}},
	})
	require.NoError(t, err)
	require.False(t, stack.IsError, "%v", stack.Content)

	search, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_search_items",
		Arguments: map[string]any{
			"query": "Test PR", "repo": map[string]any{"provider": "github", "owner": "acme", "name": "widgets"},
		},
	})
	require.NoError(t, err)
	require.False(t, search.IsError, "%v", search.Content)
	encoded, err := json.Marshal(search.StructuredContent)
	require.NoError(t, err)
	var out struct {
		Results []struct {
			Item struct {
				PlatformRepoID int64 `json:"platform_repo_id"`
				Number         int   `json:"number"`
			} `json:"item"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(encoded, &out))
	require.Len(t, out.Results, 1)
	assert.Equal(t, repoID, out.Results[0].Item.PlatformRepoID)
	assert.Equal(t, 7, out.Results[0].Item.Number)
}
