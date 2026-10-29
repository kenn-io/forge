package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/worklist"
)

// connectForgeMCP opens an MCP client session on a companion server backed by
// srv, as an agent connects to Forge.
func connectForgeMCP(t *testing.T, srv *Server) *mcp.ClientSession {
	t.Helper()
	companion, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = companion.Close() })
	endpoint := httptest.NewServer(companion.HTTPHandler())
	t.Cleanup(endpoint.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: endpoint.URL + "/mcp", DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func importWorklistOverMCP(t *testing.T, session *mcp.ClientSession, link string) (mcpserver.WorklistImportResult, *mcp.CallToolResult) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_import_worklist", Arguments: map[string]any{"link": link},
	})
	require.NoError(t, err)
	var imported mcpserver.WorklistImportResult
	if !result.IsError {
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &imported))
	}
	return imported, result
}

func TestMCPImportWorklistCreatesTheListALinkCarries(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	ref := func(itemType, owner, name string, number int) worklist.ItemRef {
		return worklist.ItemRef{Type: itemType, Provider: "github", Owner: owner, Name: name, Number: number}
	}
	link, err := worklist.KennLink(worklist.List{
		Title: "Merge today", Recipe: "open PRs with green CI", CreatedBy: "triage-bot",
		Items: []worklist.Item{
			{Item: ref("pr", "acme", "widget", 42), Score: new(9), Section: "Ready", Note: "small fix"},
			{Item: ref("pr", "other", "repo", 1), Note: "repository this Forge does not track"},
			{Item: ref("issue", "acme", "widget", 5), Caveat: "confirm repro"},
			{Item: ref("pr", "acme", "widget", 999)},
			{Item: ref("pr", "acme", "widget", 42), Note: "listed twice"},
		},
	})
	require.NoError(err)
	session := connectForgeMCP(t, srv)

	imported, result := importWorklistOverMCP(t, session, link)

	require.False(result.IsError, result.Content)
	assert.Equal(2, imported.ItemCount)
	assert.Equal(3, imported.SkippedCount)
	read, err := srv.MCPBackend().GetWorklist(ctx, imported.WorklistID)
	require.NoError(err)
	assert.Equal("Merge today", read.Title)
	assert.Equal("open PRs with green CI", read.Recipe)
	assert.Equal("triage-bot", read.CreatedBy)
	require.Len(read.Items, 2)
	assert.Equal(worklistTestItem(repo, "pr", 42), read.Items[0].Item)
	assert.Equal(new(9), read.Items[0].Score)
	assert.Equal("Ready", read.Items[0].Section)
	assert.Equal("small fix", read.Items[0].Note)
	assert.Equal(worklistTestItem(repo, "issue", 5), read.Items[1].Item)
	assert.Equal("confirm repro", read.Items[1].Caveat)

	_, err = srv.MCPBackend().DismissWorklist(ctx, imported.WorklistID)
	require.NoError(err)
	fragment, ok := strings.CutPrefix(link, worklist.KennImportURL+"#")
	require.True(ok)
	again, result := importWorklistOverMCP(t, session, "https://forge.example.com"+worklist.ImportPath+"#"+fragment)
	require.False(result.IsError, result.Content)
	assert.Equal(imported, again, "the same link opens the list it created")
	reopened, err := srv.MCPBackend().GetWorklist(ctx, imported.WorklistID)
	require.NoError(err)
	assert.Empty(reopened.DismissedAt, "importing the link again restores the dismissed list")
	listed, err := srv.MCPBackend().ListWorklists(ctx, true)
	require.NoError(err)
	assert.Len(listed, 1)
}

func TestMCPImportWorklistRejectsLinksItCannotUse(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database, _ := setupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	untracked, err := worklist.KennLink(worklist.List{Title: "Elsewhere", Items: []worklist.Item{{
		Item: worklist.ItemRef{Type: "pr", Provider: "github", Owner: "other", Name: "repo", Number: 1},
	}}})
	require.NoError(t, err)
	tracked, err := worklist.Fragment(worklist.List{Title: "Merge today", Items: []worklist.Item{{
		Item: worklist.ItemRef{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
	}}})
	require.NoError(t, err)
	session := connectForgeMCP(t, srv)
	tests := []struct {
		name string
		link string
	}{
		{"unreadable fragment", "#v1.not-a-worklist"},
		{"unknown version", worklist.KennImportURL + "#v9.AAAA"},
		{"another page", "https://forge.example.com/pulls#" + tracked},
		{"no tracked item", untracked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, result := importWorklistOverMCP(t, session, tt.link)

			require.True(t, result.IsError)
			var payload struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &payload))
			assert.Equal(t, string(httpapi.CodeValidationError), payload.Error.Code)
			assert.Equal(t, "link", payload.Error.Details["field"])
		})
	}
	listed, err := srv.MCPBackend().ListWorklists(t.Context(), true)
	require.NoError(t, err)
	assert.Empty(t, listed, "a rejected import writes nothing")
}

func TestSpokeMCPImportWorklistCreatesTheListOnTheHub(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	hubDB := dbtest.Open(t)
	serverfake.SeedPR(t, hubDB, "acme", "widget", 42)
	hubServer := New(hubDB, nil, nil, "/", nil, ServerOptions{DisableWorkspaceBackgroundMonitors: true})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, hubServer) })
	hub := httptest.NewTLSServer(hubServer)
	t.Cleanup(hub.Close)
	spoke, spokeDB := newFederatedProviderNodeForTest(
		t, "99999999999999999999999999999991", hub.URL, hub.Client(),
	)
	link, err := worklist.KennLink(worklist.List{Title: "Merge today", Items: []worklist.Item{
		{Item: worklist.ItemRef{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42}},
		{Item: worklist.ItemRef{Type: "pr", Provider: "github", Owner: "other", Name: "repo", Number: 1}},
	}})
	require.NoError(err)
	session := connectForgeMCP(t, spoke)

	imported, result := importWorklistOverMCP(t, session, link)

	require.False(result.IsError, result.Content)
	assert.Equal(1, imported.ItemCount)
	assert.Equal(1, imported.SkippedCount)
	stored, err := hubDB.GetWorklistSummary(t.Context(), imported.WorklistID, nil)
	require.NoError(err)
	assert.Equal("Merge today", stored.Title)
	again, result := importWorklistOverMCP(t, session, link)
	require.False(result.IsError, result.Content)
	assert.Equal(imported, again, "the same link opens the hub's list")
	local, err := spokeDB.ListWorklists(t.Context(), true, nil)
	require.NoError(err)
	assert.Empty(local, "spokes never write worklists locally")

	_, result = importWorklistOverMCP(t, session, "#v1.not-a-worklist")
	require.True(result.IsError)
	assert.Contains(result.Content[0].(*mcp.TextContent).Text, `"field":"link"`, "the hub's validation survives the spoke")
}
