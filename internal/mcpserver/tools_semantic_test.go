package mcpserver

import (
	"context"
	"encoding/json/v2"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func TestSemanticSearchForwardsQueryAndReturnsItemReferences(t *testing.T) {
	assert := assert.New(t)
	var got SemanticSearchQuery
	repo := RepositoryIdentity{
		Provider: "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(1001),
		RepoPath: "acme/widget", Owner: "acme", Name: "widget",
	}
	s := newMCPTestServer(t, &fakeBackend{semanticSearchFn: func(_ context.Context, query SemanticSearchQuery) (SemanticSearchResult, error) {
		got = query
		return SemanticSearchResult{Ready: true, Items: []SemanticSearchItem{{
			Repository: repo, ItemType: "pr", Number: 42, Title: "Fix flaky login test",
			URL: "https://github.com/acme/widget/pull/42", State: "open", Author: "octo",
			Score: 0.91, MatchKind: "comment", CommentID: 77, Excerpt: "fails one run in ten",
		}}}, nil
	}})
	session := connectMCPTestSession(t, s)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_semantic_search", Arguments: map[string]any{
		"query": "  flaky tests  ", "item_type": "pr", "state": "open", "limit": 500,
		"repo": map[string]any{"provider": "github", "platform_repo_id": 1001, "owner": "acme", "name": "widget"},
	}})
	require.NoError(t, err)
	require.False(t, result.IsError, "%#v", result.Content)
	raw, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var output semanticSearchOutput
	require.NoError(t, json.Unmarshal(raw, &output))

	assert.Equal("flaky tests", got.Query)
	assert.Equal("pr", got.ItemType)
	assert.Equal("open", got.State)
	assert.Equal(100, got.Limit, "limit is capped")
	assert.Equal(platform.RepositoryIDKey(1001), got.Repository.Key)
	assert.True(output.Ready)
	require.Len(t, output.Results, 1)
	hit := output.Results[0]
	assert.Equal(itemRef{
		Type: "pr", Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
		Owner: "acme", Name: "widget", RepoPath: "acme/widget", Number: 42,
		Title: "Fix flaky login test", URL: "https://github.com/acme/widget/pull/42",
		State: "open", Author: "octo",
	}, hit.Item)
	assert.Equal(semanticSearchMatch{Kind: "comment", CommentID: 77, Excerpt: "fails one run in ten"}, hit.Match)
}

func TestSemanticSearchRejectsInvalidInputBeforeCallingBackend(t *testing.T) {
	called := false
	s := newMCPTestServer(t, &fakeBackend{semanticSearchFn: func(context.Context, SemanticSearchQuery) (SemanticSearchResult, error) {
		called = true
		return SemanticSearchResult{}, nil
	}})
	session := connectMCPTestSession(t, s)
	for _, args := range []map[string]any{
		{"query": "   "},
		{"query": "x", "item_type": "commit"},
		{"query": "x", "state": "merged"},
	} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_semantic_search", Arguments: args})
		require.NoError(t, err)
		assert.True(t, result.IsError, "%v", args)
	}
	assert.False(t, called)
}
