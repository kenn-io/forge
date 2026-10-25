package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestMCPListActivityPreservesCommitAuthorAndActor(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database, _ := setupTestServer(t)
	mrID := serverfake.SeedPR(t, database, "acme", "widgets", 1)
	require.NoError(t, database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID, EventType: "commit", Summary: "1234567890abcdef1234567890abcdef12345678",
		Author: "integrator", MetadataJSON: `{"commit_author":"original-login"}`,
		CreatedAt: time.Now().UTC(), DedupeKey: "original-author",
	}}))
	toolServer, err := mcpserver.New(mcpserver.Options{Backend: srv.MCPBackend(), Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, toolServer.Close()) })
	httpServer := httptest.NewServer(toolServer.HTTPHandler())
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "activity-test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, session.Close()) })
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_list_activity", Arguments: map[string]any{"types": []string{"commit"}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	payload, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var output struct {
		Items []struct {
			Author       string `json:"author"`
			CommitAuthor string `json:"commit_author"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(payload, &output))
	require.Len(t, output.Items, 1)
	assert.Equal(t, "integrator", output.Items[0].Author)
	assert.Equal(t, "original-login", output.Items[0].CommitAuthor)
}
