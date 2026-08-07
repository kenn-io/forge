package mcpserver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisteredToolsResourcesAndPromptsAreCurated(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	s, err := New(Options{ConfigPath: filepath.Join(t.TempDir(), "config.toml"), Version: "test"})
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(s.Close())
	})
	cs := connectMCPTestSession(t, s)

	tools, err := cs.ListTools(t.Context(), nil)
	require.NoError(err)
	var toolNames []string
	for _, tool := range tools.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	slices.Sort(toolNames)
	assert.Equal([]string{
		"kenn_forge_find_review_candidates",
		"kenn_forge_get_item_context",
		"kenn_forge_get_item_diff",
		"kenn_forge_get_stack_context",
		"kenn_forge_list_activity",
		"kenn_forge_list_agent_targets",
		"kenn_forge_list_items_by_workflow_state",
		"kenn_forge_list_repos",
		"kenn_forge_list_workspace_agent_sessions",
		"kenn_forge_search_items",
		"kenn_forge_set_item_workflow_state",
		"kenn_forge_spawn_workspace_with_agent",
	}, toolNames)

	resources, err := cs.ListResources(t.Context(), nil)
	require.NoError(err)
	require.Len(resources.Resources, 1)
	assert.Equal("kenn-forge://mcp/guidance", resources.Resources[0].URI)
	assert.Equal("text/markdown", resources.Resources[0].MIMEType)
	read, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "kenn-forge://mcp/guidance"})
	require.NoError(err)
	require.Len(read.Contents, 1)
	assert.Equal("kenn-forge://mcp/guidance", read.Contents[0].URI)
	assert.Equal("text/markdown", read.Contents[0].MIMEType)
	assert.Contains(read.Contents[0].Text, "kenn_forge_find_review_candidates")
	assert.Contains(read.Contents[0].Text, "expected_status")

	prompts, err := cs.ListPrompts(t.Context(), nil)
	require.NoError(err)
	require.Len(prompts.Prompts, 1)
	assert.Equal("kenn-forge-review-candidates", prompts.Prompts[0].Name)
	prompt, err := cs.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "kenn-forge-review-candidates"})
	require.NoError(err)
	require.Len(prompt.Messages, 1)
	assert.Equal("user", string(prompt.Messages[0].Role))
	content, ok := prompt.Messages[0].Content.(*mcp.TextContent)
	require.True(ok)
	assert.Contains(content.Text, "kenn_forge_list_repos")
	assert.Contains(content.Text, "kenn_forge_get_item_diff")
	assert.Contains(content.Text, "kenn_forge_get_stack_context")
	assert.Contains(content.Text, "expected_status")
	assert.Contains(content.Text, "awaiting_merge")
	assert.Contains(content.Text, "stale")
}

func TestKennForgeMCPDocsCoverClientSetupAndSafety(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "kenn-forge-mcp.md"))
	require.NoError(err)
	text := string(data)
	normalized := strings.Join(strings.Fields(text), " ")

	required := []string{
		`"command": "kenn-forge"`,
		`"args": ["mcp"]`,
		`--transport http --addr 127.0.0.1:8092 --http-token-env KENN_FORGE_MCP_TOKEN`,
		`Authorization: Bearer`,
		`openssl rand -hex 32`,
		`cached Kenn Forge data`,
		`does not force provider refreshes`,
		`writes only Kenn Forge-local workflow state`,
		`kenn_forge_list_repos`,
		`kenn_forge_find_review_candidates`,
		`kenn_forge_search_items`,
		`kenn_forge_get_item_diff`,
		`ephemeral and local to the companion host`,
		`reviewing`,
		`expected_status`,
		`reviewing/waiting`,
		`stale cache`,
		`no Kenn Forge daemon is running on`,
		`auth_token`,
	}
	for _, want := range required {
		assert.Contains(normalized, want)
	}
}

func connectMCPTestSession(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	require := require.New(t)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := s.mcp.Connect(t.Context(), serverTransport, nil)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(serverSession.Close())
	})
	client := mcp.NewClient(&mcp.Implementation{Name: "kenn-forge-test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(clientSession.Close())
	})
	return clientSession
}
