package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/runtimelock"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestMCPStdioCLIE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	bin := buildMiddleman(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	cfgPath := filepath.Join(root, "config.toml")
	port := reserveFreePort(t)
	writeMinimalConfig(t, cfgPath, dataDir, port)
	appendConfig(t, cfgPath, `
[[repos]]
owner = "acme"
name = "widgets"

[api]
require_auth = true
`)
	database, repoID := seedMCPCLIRepo(t, dataDir)

	daemon := procutil.Command(bin, "--config", cfgPath)
	daemon.Stdout = os.Stderr
	daemon.Stderr = os.Stderr
	daemon.Env = append(os.Environ(), "MIDDLEMAN_LOG_LEVEL=warn")
	require.NoError(daemon.Start())
	t.Cleanup(func() {
		if daemon.Process != nil {
			_ = daemon.Process.Signal(syscall.SIGTERM)
			_ = daemon.Wait()
		}
	})
	waitForFile(t, runtimelock.MetadataPath(dataDir), 10*time.Second)
	waitForFile(t, runtimelock.AuthTokenPath(dataDir), 10*time.Second)
	require.Eventually(func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 100*time.Millisecond)

	client := mcp.NewClient(&mcp.Implementation{Name: "middleman-cli-e2e", Version: "test"}, nil)
	mcpCmd := procutil.Command(bin, "mcp", "--config", cfgPath, "--daemon-timeout", "5s")
	mcpCmd.Stderr = os.Stderr
	mcpCmd.Env = append(os.Environ(), "MIDDLEMAN_LOG_LEVEL=warn")
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{
		Command:           mcpCmd,
		TerminateDuration: 2 * time.Second,
	}, nil)
	require.NoError(err)
	defer func() {
		require.NoError(session.Close())
	}()

	tools, err := session.ListTools(t.Context(), nil)
	require.NoError(err)
	toolNames := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	assert.True(slices.Contains(toolNames, "middleman_list_repos"))
	assert.True(slices.Contains(toolNames, "middleman_set_item_workflow_state"))

	repos := callMCPCLITool[mcpCLIListReposOutput](t, session, "middleman_list_repos", map[string]any{})
	require.Len(repos.Repos, 1)
	assert.Equal("github", repos.Repos[0].Provider)
	assert.Equal("github.com", repos.Repos[0].PlatformHost)
	assert.Equal("acme/widgets", repos.Repos[0].RepoPath)
	assert.Equal(1, repos.Repos[0].OpenPRCount)

	item := map[string]any{
		"type":          "pr",
		"provider":      "github",
		"platform_host": "github.com",
		"owner":         "acme",
		"name":          "widgets",
		"number":        1,
	}
	workflow := callMCPCLITool[mcpCLIWorkflowOutput](t, session, "middleman_set_item_workflow_state", map[string]any{
		"item":            item,
		"status":          "reviewing",
		"expected_status": "new",
		"actor":           "mcp-stdio-e2e",
		"reason":          "claim through public stdio command",
	})
	assert.Equal("new", workflow.PreviousStatus)
	assert.Equal("reviewing", workflow.Status)

	listed := callMCPCLITool[mcpCLIWorkflowListOutput](t, session, "middleman_list_items_by_workflow_state", map[string]any{
		"states":     []string{"reviewing"},
		"item_types": []string{"pr"},
		"repo": map[string]any{
			"provider":      "github",
			"platform_host": "github.com",
			"repo_path":     "acme/widgets",
		},
	})
	require.Len(listed.Items, 1)
	assert.Equal(1, listed.Items[0].Item.Number)
	assert.Equal("reviewing", listed.Items[0].Workflow.Status)

	stored, err := database.GetItemWorkflowState(t.Context(), repoID, db.ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(string(db.KanbanStatusReviewing), stored.Status)
	assert.Equal("mcp-stdio-e2e", stored.UpdatedActor)
}

type mcpCLIListReposOutput struct {
	Repos []mcpCLIRepo `json:"repos"`
}

type mcpCLIRepo struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	OpenPRCount  int    `json:"open_pr_count"`
}

type mcpCLIWorkflowOutput struct {
	PreviousStatus string `json:"previous_status"`
	Status         string `json:"status"`
}

type mcpCLIWorkflowListOutput struct {
	Items []struct {
		Item struct {
			Number int `json:"number"`
		} `json:"item"`
		Workflow struct {
			Status string `json:"status"`
		} `json:"workflow"`
	} `json:"items"`
}

func callMCPCLITool[T any](
	t *testing.T,
	session *mcp.ClientSession,
	name string,
	args any,
) T {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "tool %s returned error content: %#v", name, result.Content)
	var out T
	if result.StructuredContent != nil {
		data, marshalErr := json.Marshal(result.StructuredContent)
		require.NoError(t, marshalErr)
		require.NoError(t, json.Unmarshal(data, &out))
		return out
	}
	require.NotEmpty(t, result.Content)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.NoError(t, json.Unmarshal([]byte(text.Text), &out))
	return out
}

func seedMCPCLIRepo(t *testing.T, dataDir string) (*db.DB, int64) {
	t.Helper()
	database := dbtest.OpenAt(t, filepath.Join(dataDir, "middleman.db"))
	repoID, err := database.UpsertRepo(
		t.Context(),
		db.GitHubRepoIdentity("github.com", "acme", "widgets"),
	)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         1,
		URL:            "https://github.com/acme/widgets/pull/1",
		Title:          "Review stdio path",
		Author:         "developer",
		State:          db.MergeRequestStateOpen,
		Body:           "cached body",
		HeadBranch:     "feature/stdio",
		BaseBranch:     "main",
		Additions:      3,
		Deletions:      1,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(t, err)
	return database, repoID
}
