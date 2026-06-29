package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

func TestLaunchWorkspaceRuntimeSessionPreparesAgentContext(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := dbtest.Open(t)
	worktree := initServerWorkspaceGitRepo(t)
	repoID := seedServerWorkspaceRepo(t, d)
	seedServerWorkspaceMR(t, d, repoID)

	ws := &db.Workspace{
		ID:              "ws-agent-context",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/widgets",
		WorkspaceBranch: "feature/widgets",
		WorktreePath:    worktree,
		TmuxSession:     "middleman-ws-agent-context",
		TerminalBackend: "tmux",
		Status:          "ready",
	}
	require.NoError(d.InsertWorkspace(t.Context(), ws))
	require.NoError(os.MkdirAll(filepath.Join(worktree, ".middleman"), 0o755))
	require.NoError(os.WriteFile(filepath.Join(worktree, ".middleman", "agent-context.md"), []byte("stale\n"), 0o644))

	tmuxPath := writeRuntimeTmuxLifecycleRecorder(t, t.TempDir(), filepath.Join(t.TempDir(), "tmux-record"))
	runtime := localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{
			{
				Key:       "codex",
				Label:     "Codex",
				Kind:      localruntime.LaunchTargetAgent,
				Command:   []string{"/bin/sh", "-c", "exit 0"},
				Available: true,
			},
			{
				Key:       string(localruntime.LaunchTargetShell),
				Label:     "tmux",
				Kind:      localruntime.LaunchTargetShell,
				Command:   []string{tmuxPath},
				Available: true,
			},
		},
		TmuxCommand:             []string{tmuxPath},
		WrapAgentSessionsInTmux: true,
	})
	server := &Server{
		db:         d,
		workspaces: workspace.NewManager(d, t.TempDir()),
		runtime:    runtime,
	}
	input := &launchWorkspaceRuntimeSessionInput{ID: ws.ID}
	input.Body.TargetKey = "codex"

	_, err := server.launchWorkspaceRuntimeSession(t.Context(), input)
	require.NoError(err)

	canonical, err := os.ReadFile(filepath.Join(worktree, ".middleman", "agent-context.md"))
	require.NoError(err)
	assert.Contains(string(canonical), "Source kind: pull request")
	assert.Contains(string(canonical), "PR: #42")
	assert.Contains(string(canonical), "Launch context")
	agentsLocal, err := os.ReadFile(filepath.Join(worktree, "AGENTS.local.md"))
	require.NoError(err)
	assert.Contains(string(agentsLocal), "Read `.middleman/agent-context.md`")
	assert.NoFileExists(filepath.Join(worktree, "CLAUDE.local.md"))
	assertServerGitIgnored(t, worktree, ".middleman/agent-context.md")
	assertServerGitIgnored(t, worktree, "AGENTS.local.md")
	status := strings.TrimSpace(string(runServerWorkspaceTestGit(t, worktree, "status", "--porcelain")))
	assert.Empty(status)
}

func seedServerWorkspaceRepo(t *testing.T, d *db.DB) int64 {
	t.Helper()
	repoID, err := d.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	return repoID
}

func seedServerWorkspaceMR(t *testing.T, d *db.DB, repoID int64) {
	t.Helper()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	_, err := d.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     4200,
		Number:         42,
		URL:            "https://github.com/acme/widget/pull/42",
		Title:          "Launch context",
		Author:         "author",
		State:          "open",
		HeadBranch:     "feature/widgets",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(t, err)
}

func initServerWorkspaceGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runServerWorkspaceTestGit(t, dir, "init", "--initial-branch=main")
	runServerWorkspaceTestGit(t, dir, "config", "user.email", "test@example.test")
	runServerWorkspaceTestGit(t, dir, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644))
	runServerWorkspaceTestGit(t, dir, "add", "README.md")
	runServerWorkspaceTestGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runServerWorkspaceTestGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
	return out
}

func assertServerGitIgnored(t *testing.T, dir, rel string) {
	t.Helper()
	runServerWorkspaceTestGit(t, dir, "check-ignore", "--quiet", "--", rel)
}
