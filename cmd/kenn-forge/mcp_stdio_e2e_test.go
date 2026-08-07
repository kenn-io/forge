package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
	forgeserver "go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestMCPStdioCommandRoundTripsRealDaemonState(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	repoIdentity := db.GitHubRepoIdentity("github.com", "acme", "widgets")
	repoIdentity.PlatformRepoID = "repo-widgets"
	repoID, err := database.UpsertRepo(ctx, repoIdentity)
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1000,
		Number:         1,
		URL:            "https://github.com/acme/widgets/pull/1",
		Title:          "Cache review",
		Author:         "reviewer",
		State:          db.MergeRequestStateOpen,
		HeadBranch:     "feature/cache",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	token := "stdio-e2e-token"
	syncer := ghclient.NewSyncer(
		nil,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widgets",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	apiServer := forgeserver.New(database, syncer, nil, "/", nil, forgeserver.ServerOptions{
		DaemonAccess: forgeserver.DaemonAccessOptions{
			Token: token, RequireAPIAuth: true,
		},
	})
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(apiServer.Shutdown(shutdownCtx))
	})
	httpServer := httptest.NewServer(apiServer)
	t.Cleanup(httpServer.Close)

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "config.toml")
	require.NoError(os.WriteFile(
		configPath,
		fmt.Appendf(nil, "data_dir = %q\n", dataDir),
		0o600,
	))
	require.NoError(os.WriteFile(
		runtimelock.AuthTokenPath(dataDir), []byte(token+"\n"), 0o600,
	))
	handle, err := runtimelock.Acquire(dataDir)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(handle.Release()) })
	parsed, err := url.Parse(httpServer.URL)
	require.NoError(err)
	host, portText, err := net.SplitHostPort(parsed.Host)
	require.NoError(err)
	port, err := strconv.Atoi(portText)
	require.NoError(err)
	require.NoError(handle.WriteMetadata(runtimelock.Metadata{
		PID:         os.Getpid(),
		Host:        host,
		Port:        port,
		ListenAddr:  parsed.Host,
		StartedAt:   now.Format(time.RFC3339),
		Version:     "test",
		TokenPath:   runtimelock.AuthTokenPath(dataDir),
		BasePath:    "/",
		RequireAuth: true,
	}))

	command := procutil.Command(os.Args[0], "-test.run=^TestMCPStdioCommandHelper$")
	command.Env = append(os.Environ(),
		"KENN_FORGE_MCP_STDIO_HELPER=1",
		"KENN_FORGE_MCP_STDIO_CONFIG="+configPath,
	)
	command.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{
		Name: "kenn-forge-command-e2e", Version: "test",
	}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(session.Close()) })

	repos := callCommandMCPTool[struct {
		Repos []struct {
			Provider string `json:"provider"`
			RepoPath string `json:"repo_path"`
		} `json:"repos"`
	}](t, session, "kenn_forge_list_repos", map[string]any{})
	require.Len(repos.Repos, 1)
	assert.Equal("github", repos.Repos[0].Provider)
	assert.Equal("acme/widgets", repos.Repos[0].RepoPath)

	claim := callCommandMCPTool[struct {
		PreviousStatus string `json:"previous_status"`
		Status         string `json:"status"`
	}](t, session, "kenn_forge_set_item_workflow_state", map[string]any{
		"item": map[string]any{
			"type": "pr", "provider": "github",
			"platform_host": "github.com",
			"owner":         "acme", "name": "widgets", "number": 1,
		},
		"status":          "reviewing",
		"expected_status": "new",
		"actor":           "stdio-command-e2e",
	})
	assert.Equal("new", claim.PreviousStatus)
	assert.Equal("reviewing", claim.Status)
	stored, err := database.GetItemWorkflowState(ctx, repoID, db.ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("reviewing", stored.Status)
	assert.Equal("stdio-command-e2e", stored.UpdatedActor)
}

func TestMCPStdioCommandHelper(t *testing.T) {
	if os.Getenv("KENN_FORGE_MCP_STDIO_HELPER") != "1" {
		return
	}
	root := newRootCommand(cliOptions{})
	root.SetArgs([]string{
		"mcp", "--config", os.Getenv("KENN_FORGE_MCP_STDIO_CONFIG"),
	})
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func callCommandMCPTool[T any](
	t *testing.T,
	session *mcp.ClientSession,
	name string,
	args any,
) T {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: name, Arguments: args,
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "tool %s returned error: %#v", name, result.Content)
	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var output T
	require.NoError(t, json.Unmarshal(data, &output))
	return output
}
