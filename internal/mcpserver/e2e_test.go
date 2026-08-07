package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/runtimelock"
	forgeserver "go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestMCPToolsRoundTripAgainstDaemonAPI(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	dataDir := t.TempDir()
	cfgPath := filepath.Join(dataDir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, "data_dir = %q\n", dataDir), 0o600))
	token := "mcp-e2e-token"
	require.NoError(os.WriteFile(runtimelock.AuthTokenPath(dataDir), []byte(token+"\n"), 0o600))

	database := dbtest.Open(t)
	repoID, prID := seedMCPPR(t, database, 1, "Cache review")
	_, basePRID := seedMCPPR(t, database, 2, "Base dependency")
	issueID := seedMCPIssue(t, database, 3, "Cache follow-up")
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "please inspect the cache change",
		CreatedAt:      time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second),
		DedupeKey:      "mcp-e2e-pr-comment",
	}}))
	require.NoError(database.UpsertIssueEvents(ctx, []db.IssueEvent{{
		IssueID:   issueID,
		EventType: "comment",
		Author:    "triager",
		Body:      "cache follow-up needs a look",
		CreatedAt: time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second),
		DedupeKey: "mcp-e2e-issue-comment",
	}}))
	stackID, err := database.UpsertStack(ctx, repoID, 2, "cache-stack")
	require.NoError(err)
	require.NoError(database.ReplaceStackMembers(ctx, stackID, []db.StackMember{
		{StackID: stackID, MergeRequestID: basePRID, Position: 1},
		{StackID: stackID, MergeRequestID: prID, Position: 2},
	}))
	initialWorkflow, err := database.GetItemWorkflowState(ctx, repoID, db.ItemTypePR, 1)
	require.NoError(err)
	require.Nil(initialWorkflow, "MCP e2e claim must prove expected_status=new against missing workflow storage")

	diffRepo, err := testutil.SetupDiffRepo(ctx, t.TempDir(), database)
	require.NoError(err)
	agentDisabled := false
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key: "codex", Label: "Codex", Command: []string{"codex"}, Enabled: &agentDisabled,
		}},
		PullRequests: config.PullRequests{PreferGitHubNativeStacks: true},
		Tmux:         config.Tmux{Command: []string{"kenn-forge-test-missing-tmux"}},
	}
	syncer := ghclient.NewSyncer(
		nil,
		database,
		nil,
		[]ghclient.RepoRef{{Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "widgets"}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	apiServer := forgeserver.New(database, syncer, nil, "/", cfg, forgeserver.ServerOptions{
		DaemonAccess: forgeserver.DaemonAccessOptions{
			Token: token, RequireAPIAuth: true,
		},
		Clones:                             diffRepo.Manager,
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
		DisableWorkspaceEnrichment:         true,
		HostCheck: forgeserver.HostCheckOptions{
			Bind: config.HostKey{Host: "127.0.0.1", Port: "8080"},
		},
		HostCheckAllowLoopbackAnyPort: true,
	})
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(apiServer.Shutdown(shutdownCtx))
	})
	httpServer := httptest.NewServer(apiServer)
	t.Cleanup(httpServer.Close)
	writeMCPRuntimeMetadata(t, dataDir, httpServer.URL)

	mcpServer, err := New(Options{ConfigPath: cfgPath, DaemonTimeout: 5 * time.Second, Version: "test"})
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(mcpServer.Close())
	})
	session := connectMCPTestSession(t, mcpServer)
	targets := callMCPTool[listAgentTargetsOutput](t, session, "kenn_forge_list_agent_targets", map[string]any{})
	codexTarget, found := findAgentTarget(targets.Targets, "codex")
	require.True(found)
	assert.False(codexTarget.Available)
	assert.NotEmpty(codexTarget.DisabledReason)

	repos := callMCPTool[listReposOutput](t, session, "kenn_forge_list_repos", map[string]any{})
	require.Len(repos.Repos, 1)
	assert.Equal("github", repos.Repos[0].Provider)
	assert.Equal("github.com", repos.Repos[0].PlatformHost)
	assert.Equal("acme/widgets", repos.Repos[0].RepoPath)

	search := callMCPTool[searchItemsOutput](t, session, "kenn_forge_search_items", map[string]any{
		"query":      "Cache",
		"item_types": []string{"pr"},
		"repo": map[string]any{
			"provider":      "github",
			"platform_host": "github.com",
			"repo_path":     "acme/widgets",
		},
	})
	require.NotEmpty(search.Results)
	assert.Equal(1, search.Results[0].Item.Number)
	assert.Equal("new", search.Results[0].WorkflowStatus)

	candidates := callMCPTool[findCandidatesOutput](t, session, "kenn_forge_find_review_candidates", map[string]any{
		"since":      "2h",
		"item_types": []string{"pr", "issue"},
		"repo": map[string]any{
			"provider":      "github",
			"platform_host": "github.com",
			"repo_path":     "acme/widgets",
		},
	})
	require.NotEmpty(candidates.Candidates)
	assert.Equal("issue", candidates.Candidates[0].Item.Type)
	assert.Equal("pr", candidates.Candidates[1].Item.Type)
	assert.True(candidates.Candidates[1].Stack.Present)

	item := map[string]any{
		"type":          "pr",
		"provider":      "github",
		"platform_host": "github.com",
		"owner":         "acme",
		"name":          "widgets",
		"number":        1,
	}
	contextOut := callMCPTool[getItemContextOutput](t, session, "kenn_forge_get_item_context", map[string]any{
		"item":          item,
		"event_limit":   1,
		"include_stack": true,
	})
	assert.Equal("Cache review", contextOut.Item.Title)
	assert.Equal("new", contextOut.Workflow.Status)
	require.Len(contextOut.Events, 1)
	assert.Equal("reviewer", contextOut.Events[0].Author)

	diffItems := []map[string]any{
		item,
		{
			"type":     "pr",
			"provider": "github",
			"owner":    "acme",
			"name":     "widgets",
			"number":   1,
		},
		{
			"type":     "pr",
			"provider": "gh",
			"owner":    "acme",
			"name":     "widgets",
			"number":   1,
		},
		{
			"type":          "pr",
			"provider":      "Gh",
			"platform_host": "GITHUB.COM",
			"owner":         "Acme",
			"name":          "Widgets",
			"number":        1,
		},
	}
	var diffPath string
	for i, diffItem := range diffItems {
		diff := callMCPTool[getItemDiffOutput](t, session, "kenn_forge_get_item_diff", map[string]any{
			"item":           diffItem,
			"emit_diff_file": true,
		})
		require.NotEmpty(diff.Files)
		require.NotNil(diff.DiffFile)
		if i == 0 {
			diffPath = diff.DiffFile.Path
			continue
		}
		assert.Equal(diffPath, diff.DiffFile.Path)
	}
	diffData, err := os.ReadFile(diffPath)
	require.NoError(err)
	assert.Contains(string(diffData), "diff --git")

	longHost := "self-hosted-" + strings.Repeat("segment-", 8) + "git.example.test"
	longOwner := strings.TrimSuffix(strings.Repeat("nested-group/", 12), "/")
	longName := strings.Repeat("repository", 12)
	longNumber := 77
	longRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   longHost,
		PlatformRepoID: "repo-long",
		Owner:          longOwner,
		Name:           longName,
		RepoPath:       longOwner + "/" + longName,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          longRepoID,
		PlatformID:      77000,
		Number:          longNumber,
		URL:             "https://" + longHost + "/" + longOwner + "/" + longName + "/pull/77",
		Title:           "Long diff identity",
		Author:          "testuser",
		State:           db.MergeRequestStateOpen,
		Body:            "cached PR body",
		HeadBranch:      "feature/caching",
		BaseBranch:      "main",
		PlatformHeadSHA: diffRepo.HeadSHA,
		PlatformBaseSHA: diffRepo.BaseSHA,
		Additions:       5,
		Deletions:       2,
		CreatedAt:       time.Now().UTC().Truncate(time.Second),
		UpdatedAt:       time.Now().UTC().Truncate(time.Second),
		LastActivityAt:  time.Now().UTC().Truncate(time.Second),
	})
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, longRepoID, longNumber, diffRepo.HeadSHA, diffRepo.BaseSHA, diffRepo.BaseSHA))
	require.NoError(database.UpdatePlatformSHAs(ctx, longRepoID, longNumber, diffRepo.HeadSHA, diffRepo.BaseSHA))
	sourceClone, err := diffRepo.Manager.ClonePath("github", "github.com", "acme", "widgets")
	require.NoError(err)
	longClone, err := diffRepo.Manager.ClonePath("github", longHost, longOwner, longName)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(longClone), 0o755))
	require.NoError(os.Rename(sourceClone, longClone))
	require.NoError(database.UpdateRepoProviderMetadata(ctx, longRepoID, db.RepoProviderMetadata{
		WebURL:        "https://" + longHost + "/" + longOwner + "/" + longName,
		CloneURL:      longClone,
		DefaultBranch: "main",
	}))
	longDiff := callMCPTool[getItemDiffOutput](t, session, "kenn_forge_get_item_diff", map[string]any{
		"item": map[string]any{
			"type":          "pr",
			"provider":      "github",
			"platform_host": longHost,
			"owner":         longOwner,
			"name":          longName,
			"number":        longNumber,
		},
		"emit_diff_file": true,
	})
	require.NotNil(longDiff.DiffFile)
	assert.LessOrEqual(len(filepath.Base(longDiff.DiffFile.Path)), maxMCPDiffFileNameBytes)
	longDiffData, err := os.ReadFile(longDiff.DiffFile.Path)
	require.NoError(err)
	assert.Contains(string(longDiffData), "diff --git")

	stack := callMCPTool[getStackContextOutput](t, session, "kenn_forge_get_stack_context", map[string]any{
		"item": item,
	})
	require.True(stack.Present)
	require.Len(stack.Members, 2)
	assert.Equal(1, stack.Members[1].Number)
	assert.True(stack.Members[1].IsRequested)

	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:              "ws-mcp-agent-sessions",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widgets",
		ItemType:        db.WorkspaceItemTypeAdHoc,
		ItemKey:         db.AdHocWorkspaceItemKey("mcp-sessions"),
		ItemNumber:      0,
		GitHeadRef:      "mcp-sessions",
		WorkspaceBranch: "mcp-sessions",
		WorktreePath:    filepath.Join(t.TempDir(), "workspace"),
		TmuxSession:     "kenn-forge-mcp-agent-sessions",
		Status:          "ready",
	}))
	liveSessions := callMCPTool[listWorkspaceAgentSessionsOutput](
		t, session, "kenn_forge_list_workspace_agent_sessions",
		map[string]any{"workspace_id": "ws-mcp-agent-sessions"},
	)
	assert.Empty(liveSessions.Sessions)

	claim := callMCPTool[setWorkflowOutput](t, session, "kenn_forge_set_item_workflow_state", map[string]any{
		"item":            item,
		"status":          "reviewing",
		"expected_status": "new",
		"actor":           "mcp-e2e",
		"reason":          "reviewing cache change",
	})
	assert.Equal("new", claim.PreviousStatus)
	assert.Equal("reviewing", claim.Status)
	assert.Equal("mcp", claim.UpdatedSource)
	storedWorkflow, err := database.GetItemWorkflowState(ctx, repoID, db.ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(storedWorkflow)
	assert.Equal(string(db.KanbanStatusReviewing), storedWorkflow.Status)
	assert.Equal("mcp", storedWorkflow.UpdatedSource)
	assert.Equal("mcp-e2e", storedWorkflow.UpdatedActor)

	listed := callMCPTool[listByWorkflowOutput](t, session, "kenn_forge_list_items_by_workflow_state", map[string]any{
		"states": []string{"reviewing"},
		"repo": map[string]any{
			"provider":      "github",
			"platform_host": "github.com",
			"repo_path":     "acme/widgets",
		},
	})
	require.Len(listed.Items, 1)
	assert.Equal(1, listed.Items[0].Item.Number)
	assert.Equal("reviewing", listed.Items[0].Workflow.Status)

	status := daemonWorkflowState(t, httpServer.URL, token)
	require.Len(status.Items, 1)
	assert.Equal("reviewing", status.Items[0].Workflow.Status)
	assert.Equal("mcp", status.Items[0].Workflow.UpdatedSource)
	assert.Equal("mcp-e2e", status.Items[0].Workflow.UpdatedActor)

	t.Setenv("KENN_FORGE_MCP_HTTP_TOKEN", "streamable-e2e-token")
	httpMCPServer, err := New(Options{
		ConfigPath:    cfgPath,
		Transport:     "http",
		Addr:          "127.0.0.1:0",
		HTTPTokenEnv:  "KENN_FORGE_MCP_HTTP_TOKEN",
		DaemonTimeout: 5 * time.Second,
		Version:       "test",
	})
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(httpMCPServer.Close())
	})
	httpCtx, httpCancel := context.WithCancel(context.Background())
	httpErrc := make(chan error, 1)
	go func() {
		httpErrc <- httpMCPServer.RunHTTP(httpCtx)
	}()
	t.Cleanup(func() {
		httpCancel()
		require.NoError(<-httpErrc)
	})
	httpSession := connectHTTPMCPTestSession(t, httpMCPServer, "streamable-e2e-token")
	t.Cleanup(func() {
		err := httpSession.Close()
		if err != nil && !strings.Contains(err.Error(), "EOF") {
			require.NoError(err)
		}
	})

	httpListed := callMCPTool[listByWorkflowOutput](t, httpSession, "kenn_forge_list_items_by_workflow_state", map[string]any{
		"states": []string{"reviewing"},
		"repo": map[string]any{
			"provider":      "github",
			"platform_host": "github.com",
			"repo_path":     "acme/widgets",
		},
	})
	require.Len(httpListed.Items, 1)
	assert.Equal(1, httpListed.Items[0].Item.Number)

	httpConflict := callMCPToolError(t, httpSession, "kenn_forge_set_item_workflow_state", map[string]any{
		"item":            item,
		"status":          "waiting",
		"expected_status": "new",
		"actor":           "mcp-e2e",
		"reason":          "stale http claim",
	})
	assert.Contains(httpConflict, "conflict")

	httpForced := callMCPTool[setWorkflowOutput](t, httpSession, "kenn_forge_set_item_workflow_state", map[string]any{
		"item":   item,
		"status": "waiting",
		"force":  true,
		"actor":  "mcp-http-e2e",
		"reason": "deliberate http override",
	})
	assert.Equal("reviewing", httpForced.PreviousStatus)
	assert.Equal("waiting", httpForced.Status)
	httpMissingGuard := callMCPToolError(t, httpSession, "kenn_forge_set_item_workflow_state", map[string]any{
		"item":   item,
		"status": "reviewing",
	})
	assert.Contains(httpMissingGuard, "expected_status")

	storedWorkflow, err = database.GetItemWorkflowState(ctx, repoID, db.ItemTypePR, 1)
	require.NoError(err)
	require.NotNil(storedWorkflow)
	assert.Equal(string(db.KanbanStatusWaiting), storedWorkflow.Status)
	assert.Equal("mcp-http-e2e", storedWorkflow.UpdatedActor)
}

func callMCPTool[T any](
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

func callMCPToolError(
	t *testing.T,
	session *mcp.ClientSession,
	name string,
	args any,
) string {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return err.Error()
	}
	if result.IsError {
		var b strings.Builder
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				b.WriteString(text.Text)
				continue
			}
			fmt.Fprintf(&b, "%T", content)
		}
		return b.String()
	}
	require.FailNow(t, "expected tool error", "tool %s returned success: %#v", name, result)
	return ""
}

func connectHTTPMCPTestSession(t *testing.T, s *Server, token string) *mcp.ClientSession {
	t.Helper()
	endpoint := "http://" + waitForHTTPAddr(t, s)
	client := mcp.NewClient(&mcp.Implementation{Name: "kenn-forge-http-test-client", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearerRoundTripper{token: token}},
		DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	return session
}

func daemonWorkflowState(t *testing.T, baseURL string, token string) daemonWorkflowStateResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		baseURL+"/api/v1/workflow-state?item_type=pr&state=reviewing&repo=github%7Cgithub.com%2Facme%2Fwidgets",
		nil,
	)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out daemonWorkflowStateResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func writeMCPRuntimeMetadata(t *testing.T, dataDir string, rawURL string) {
	t.Helper()
	handle, err := runtimelock.Acquire(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, handle.Release())
	})
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	host, portText, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	require.NoError(t, handle.WriteMetadata(runtimelock.Metadata{
		PID:         os.Getpid(),
		Host:        host,
		Port:        port,
		ListenAddr:  parsed.Host,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		Version:     "test",
		TokenPath:   runtimelock.AuthTokenPath(dataDir),
		BasePath:    "/",
		RequireAuth: true,
	}))
}

func seedMCPPR(t *testing.T, database *db.DB, number int, title string) (int64, int64) {
	t.Helper()
	ctx := t.Context()
	identity := db.GitHubRepoIdentity("github.com", "acme", "widgets")
	identity.PlatformRepoID = "repo-widgets"
	repoID, err := database.UpsertRepo(ctx, identity)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      int64(number) * 1000,
		Number:          number,
		URL:             fmt.Sprintf("https://github.com/acme/widgets/pull/%d", number),
		Title:           title,
		Author:          "testuser",
		State:           db.MergeRequestStateOpen,
		Body:            "cached PR body",
		HeadBranch:      "feature/caching",
		BaseBranch:      "main",
		PlatformHeadSHA: "",
		PlatformBaseSHA: "",
		Additions:       5,
		Deletions:       2,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	}
	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(t, err)
	return repoID, prID
}

func seedMCPIssue(t *testing.T, database *db.DB, number int, title string) int64 {
	t.Helper()
	ctx := t.Context()
	identity := db.GitHubRepoIdentity("github.com", "acme", "widgets")
	identity.PlatformRepoID = "repo-widgets"
	repoID, err := database.UpsertRepo(ctx, identity)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	issueID, err := database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     int64(number) * 1000,
		Number:         number,
		URL:            fmt.Sprintf("https://github.com/acme/widgets/issues/%d", number),
		Title:          title,
		Author:         "reporter",
		State:          "open",
		Body:           "cached issue body",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(t, err)
	return issueID
}
