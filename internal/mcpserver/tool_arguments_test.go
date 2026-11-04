package mcpserver

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Agents and generated apps copy a kenn_forge_list_repos row, counts and
// all, into the next tool's repository argument.
func TestRepositoryArgumentsAcceptListRepoRowsAndReportIgnoredFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	repo := testRepository()
	var listed []ItemListQuery
	var fetched []ItemIdentity
	backend := &fakeBackend{
		listRepositoriesFn: func(context.Context) ([]RepositorySummary, error) {
			return []RepositorySummary{{
				Repository: repo, OpenPRCount: 3, OpenIssueCount: 2,
				LastSyncCompletedAt: "2026-11-01T12:00:00Z",
			}}, nil
		},
		listPullsFn: func(_ context.Context, query ItemListQuery) ([]Pull, error) {
			listed = append(listed, query)
			return nil, nil
		},
		getPullFn: func(_ context.Context, item ItemIdentity) (PullDetail, error) {
			fetched = append(fetched, item)
			pull := Pull{Number: item.Number, Repository: repo}
			return PullDetail{Pull: &pull}, nil
		},
	}
	client := connectMCPTestSession(t, newMCPTestServer(t, backend))
	repos, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_list_repos"})
	require.NoError(err)
	require.False(repos.IsError)
	var listing struct {
		Repos []map[string]any `json:"repos"`
	}
	encoded, err := json.Marshal(repos.StructuredContent)
	require.NoError(err)
	require.NoError(json.Unmarshal(encoded, &listing))
	require.Len(listing.Repos, 1)
	row := listing.Repos[0]

	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_list_pull_contexts", Arguments: map[string]any{"repo": row},
	})
	require.NoError(err)
	require.False(result.IsError, "%v", result.Content)
	require.Len(listed, 1)
	assert.Equal(repo, listed[0].Repository)
	ignored := []string{"repo.last_sync_completed_at", "repo.open_issue_count", "repo.open_pr_count"}
	assert.Equal(ignored, ignoredArgumentsMeta(t, result))
	require.Len(result.Content, 2)
	assert.True(jsontext.Value(result.Content[0].(*mcp.TextContent).Text).IsValid(), "first block keeps the JSON result")
	assert.Contains(result.Content[1].(*mcp.TextContent).Text, "repo.open_pr_count")

	result, err = client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_get_item_context",
		Arguments: map[string]any{
			"item": map[string]any{"type": "pr", "number": 7, "repository": row}, "include_events": false,
		},
	})
	require.NoError(err)
	require.False(result.IsError, "%v", result.Content)
	require.Len(fetched, 1)
	assert.Equal(7, fetched[0].Number)
	assert.Equal(repo.Owner, fetched[0].Owner)
	assert.Equal([]string{
		"item.repository.last_sync_completed_at", "item.repository.open_issue_count",
		"item.repository.open_pr_count",
	}, ignoredArgumentsMeta(t, result))
}

func TestToolArgumentsStayStrictOutsideRepositoryObjects(t *testing.T) {
	client := connectMCPTestSession(t, newMCPTestServer(t, &fakeBackend{}))
	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{name: "top-level field", args: map[string]any{"stat": "open", "repo": map[string]any{"owner": "acme", "name": "widget"}}, field: "stat"},
		{name: "wrong type inside repo", args: map[string]any{"repo": map[string]any{"owner": "acme", "name": "widget", "platform_repo_id": "1001"}}, field: "platform_repo_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "kenn_forge_list_pull_contexts", Arguments: tc.args,
			})
			require.NoError(t, err)
			assert.True(t, result.IsError)
			assert.Contains(t, toolResultText(result), tc.field)
		})
	}
}

func TestCleanArgumentsReportNoIgnoredFields(t *testing.T) {
	backend := &fakeBackend{listPullsFn: func(context.Context, ItemListQuery) ([]Pull, error) { return nil, nil }}
	client := connectMCPTestSession(t, newMCPTestServer(t, backend))
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "kenn_forge_list_pull_contexts",
		Arguments: map[string]any{"repo": map[string]any{"provider": "github", "platform_repo_id": 1001, "owner": "acme", "name": "widget"}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "%v", result.Content)
	assert.NotContains(t, result.Meta, ignoredArgumentsMetaKey)
	assert.Len(t, result.Content, 1)
}

func ignoredArgumentsMeta(t *testing.T, result *mcp.CallToolResult) []string {
	t.Helper()
	encoded, err := json.Marshal(result.Meta[ignoredArgumentsMetaKey])
	require.NoError(t, err)
	var ignored []string
	require.NoError(t, json.Unmarshal(encoded, &ignored))
	return ignored
}

func toolResultText(result *mcp.CallToolResult) string {
	var text string
	for _, content := range result.Content {
		if block, ok := content.(*mcp.TextContent); ok {
			text += block.Text + "\n"
		}
	}
	return text
}
