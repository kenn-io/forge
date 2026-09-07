package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
)

func TestRetryPreservesWorkspaceCommitsAndUntrackedFiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	mgr := newTestManager(t, database, t.TempDir())
	mgr.SetClones(gitclone.New(t.TempDir(), nil))
	clone, err := mgr.clones.ClonePath("github", "github.com", "acme", "widget")
	require.NoError(err)
	seedWorkspaceBareCloneAt(t, clone)
	path := filepath.Join(t.TempDir(), "workspace")
	branch := "feature/saved-work"
	runWorkspaceTestGit(t, clone, "worktree", "add", path, "-b", branch, "HEAD")
	runWorkspaceTestGit(t, path, "commit", "--allow-empty", "-m", "saved work")
	before, ok, err := gitRefSHA(ctx, path, "HEAD")
	require.NoError(err)
	require.True(ok)
	require.NoError(os.WriteFile(filepath.Join(path, "notes.txt"), []byte("unfinished work"), 0o600))
	ws := &Workspace{ID: "saved-workspace", Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 7, GitHeadRef: branch, WorkspaceBranch: branch, WorktreePath: path, Status: "error"}
	require.NoError(database.InsertWorkspace(ctx, ws))
	require.NoError(writeWorkspaceOwnershipMarker(ctx, clone, ws))
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
		WorkspaceID: ws.ID, SessionKey: "saved-agent", TargetKey: "codex", Kind: "agent", Scope: "session", TmuxSession: "saved-tmux",
	}))
	retried, started, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.True(started)
	assert.Equal(branch, retried.WorkspaceBranch)
	sessions, err := database.ListWorkspaceRuntimeSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(sessions, 1)
	assert.Equal("saved-agent", sessions[0].SessionKey)
	assert.FileExists(filepath.Join(path, "notes.txt"))
	after, ok, err := gitRefSHA(ctx, clone, "refs/heads/"+branch)
	require.NoError(err)
	assert.True(ok)
	assert.Equal(before, after)
}

func TestIssueRecoveryChecksOutExistingBranchWithSavedCommits(t *testing.T) {
	require := require.New(t)
	mgr := newTestManager(t, openTestDB(t), t.TempDir())
	clone := filepath.Join(t.TempDir(), "clone.git")
	seedWorkspaceBareCloneAt(t, clone)
	configureOriginHeadForIssueWorkspace(t, clone)
	branch := "feature/saved-work"
	old := filepath.Join(t.TempDir(), "old")
	runWorkspaceTestGit(t, clone, "worktree", "add", old, "-b", branch, "HEAD")
	runWorkspaceTestGit(t, old, "commit", "--allow-empty", "-m", "saved work")
	before, _, err := gitRefSHA(t.Context(), old, "HEAD")
	require.NoError(err)
	runWorkspaceTestGit(t, clone, "worktree", "remove", old)
	ws := &Workspace{ID: "recover-branch", ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 7, GitHeadRef: branch, WorkspaceBranch: branch, WorktreePath: filepath.Join(t.TempDir(), "restored")}
	_, err = mgr.addIssueWorktree(t.Context(), workspaceGitDir{path: clone, remote: originRemoteName}, ws)
	require.NoError(err)
	after, _, err := gitRefSHA(t.Context(), ws.WorktreePath, "HEAD")
	require.NoError(err)
	assert.Equal(t, before, after)
}

func TestNewKataWorkspaceDoesNotAdoptCollidingBranch(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	seedRepo(t, database, "github.com", "acme", "widget")
	mgr := newTestManager(t, database, t.TempDir())
	ws, err := mgr.CreateKataTask(t.Context(), "github", "github.com", "acme", "widget", db.WorkspaceKataMetadata{
		DaemonID: "daemon-a", ProjectUID: "project-a", IssueUID: "task-a", ShortID: "task-1", Title: "Fix widget",
	})
	require.NoError(err)
	mgr.SetClones(gitclone.New(t.TempDir(), nil))
	clone, err := mgr.clones.ClonePath("github", "github.com", "acme", "widget")
	require.NoError(err)
	tmux, _ := writeRecorderScript(t)
	mgr.SetTmuxCommand([]string{tmux})
	seedWorkspaceBareCloneAt(t, clone)
	configureOriginHeadForIssueWorkspace(t, clone)
	runWorkspaceTestGit(t, clone, "branch", ws.GitHeadRef, "HEAD")
	before, _, err := gitRefSHA(t.Context(), clone, "refs/heads/"+ws.GitHeadRef)
	require.NoError(err)
	_, _, err = mgr.addWorktree(t.Context(), workspaceGitDir{path: clone, remote: originRemoteName}, ws, workspaceGitFetchOptions{})
	require.Error(err, "initial setup must not adopt a colliding user branch")
	_, err = mgr.Delete(t.Context(), ws.ID, true, nil)
	require.NoError(err)
	after, _, err := gitRefSHA(t.Context(), clone, "refs/heads/"+ws.GitHeadRef)
	require.NoError(err)
	assert.Equal(t, before, after)
	assert.NoDirExists(t, ws.WorktreePath)
}

func TestMissingAdoptedCheckoutRetainsHeadAcrossFailedRecovery(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(fmt.Sprintf("detached=%t", detached), func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()
			mgr := newTestManager(t, openTestDB(t), t.TempDir())
			clone := filepath.Join(t.TempDir(), "clone.git")
			seedWorkspaceBareCloneAt(t, clone)
			path := filepath.Join(t.TempDir(), "workspace")
			args := []string{"worktree", "add", path, "-b", "saved-branch", "HEAD"}
			if detached {
				args = []string{"worktree", "add", "--detach", path, "HEAD"}
			}
			runWorkspaceTestGit(t, clone, args...)
			runWorkspaceTestGit(t, path, "commit", "--allow-empty", "-m", "saved work")
			before := runWorkspaceTestGit(t, path, "rev-parse", "HEAD")
			require.NoError(os.WriteFile(filepath.Join(path, "staged.txt"), []byte("staged work\n"), 0o600))
			runWorkspaceTestGit(t, path, "add", "staged.txt")
			ws := &Workspace{ID: "saved-checkout", ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7, GitHeadRef: "provider-branch", WorktreePath: path}
			metadata, err := worktreeGitDir(ctx, path)
			require.NoError(err)
			require.NoError(os.RemoveAll(path))
			// A blocked ownership-marker write is a post-checkout failure.
			marker := filepath.Join(metadata, workspaceOwnershipMarkerFile)
			require.NoError(os.Mkdir(marker, 0o755))
			_, _, err = mgr.addWorktree(ctx, workspaceGitDir{path: clone, remote: originRemoteName}, ws, workspaceGitFetchOptions{})
			require.Error(err)
			assert.NoDirExists(path)
			require.NoError(os.Remove(marker))
			branch, _, err := mgr.addWorktree(ctx, workspaceGitDir{path: clone, remote: originRemoteName}, ws, workspaceGitFetchOptions{})
			require.NoError(err)
			assert.Empty(branch, "adoption must not gain branch ownership")
			assert.Equal(before, runWorkspaceTestGit(t, path, "rev-parse", "HEAD"))
			current, err := worktreeCurrentBranch(ctx, path)
			require.NoError(err)
			if detached {
				assert.Empty(current)
			} else {
				assert.Equal("saved-branch", current)
			}
			staged, err := os.ReadFile(filepath.Join(path, "staged.txt"))
			require.NoError(err)
			assert.Equal("staged work\n", string(staged))
			assert.Contains(string(runWorkspaceTestGit(t, path, "diff", "--cached", "--name-only")), "staged.txt")
		})
	}
}

func TestSetupReusesAdoptedPRCheckoutWithLocalCommits(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(fmt.Sprintf("detached=%t", detached), func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()
			database := openTestDB(t)
			localRepo, _, host := setupHTTPWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
			repoID := seedRepo(t, database, host, "acme", "widget")
			seedMR(t, database, repoID, 42, "feature/thing")
			mgr := newTestManager(t, database, t.TempDir())
			tmux, _ := writeRecorderScript(t)
			mgr.SetTmuxCommand([]string{tmux})
			mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))
			ws, err := mgr.Create(ctx, "github", host, "acme", "widget", 42)
			require.NoError(err)
			args := []string{"worktree", "add", ws.WorktreePath, "feature/thing"}
			if detached {
				args = []string{"worktree", "add", "--detach", ws.WorktreePath, "origin/feature/thing"}
			}
			runWorkspaceTestGit(t, localRepo, args...)
			runWorkspaceTestGit(t, ws.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "saved work")
			before := runWorkspaceTestGit(t, ws.WorktreePath, "rev-parse", "HEAD")
			ws.WorkspaceBranch = ""
			require.NoError(database.UpdateWorkspaceBranch(ctx, ws.ID, ""))
			require.NoError(mgr.Setup(ctx, ws))
			got, err := database.GetWorkspace(ctx, ws.ID)
			require.NoError(err)
			assert.Equal("ready", got.Status)
			assert.Empty(got.WorkspaceBranch)
			assert.Equal(before, runWorkspaceTestGit(t, ws.WorktreePath, "rev-parse", "HEAD"))
		})
	}
}

func TestRestoredAdHocCheckoutSurvivesLaterSetupFailure(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	localRepo, _, host := setupHTTPWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	seedRepo(t, database, host, "acme", "widget")
	runWorkspaceTestGit(t, localRepo, "branch", "saved-branch", "origin/feature/thing")
	mgr := newTestManager(t, database, t.TempDir())
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))
	ws, err := mgr.CreateAdHoc(ctx, "github", host, "acme", "widget", CreateAdHocOptions{BranchName: "saved-branch", ReuseExistingBranch: true})
	require.NoError(err)
	require.Empty(ws.WorkspaceBranch)
	runWorkspaceTestGit(t, localRepo, "worktree", "add", ws.WorktreePath, "saved-branch")
	runWorkspaceTestGit(t, ws.WorktreePath, "checkout", "--detach")
	runWorkspaceTestGit(t, ws.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "saved work")
	before := runWorkspaceTestGit(t, ws.WorktreePath, "rev-parse", "HEAD")
	require.NoError(os.WriteFile(filepath.Join(ws.WorktreePath, "staged.txt"), []byte("saved stage\n"), 0o600))
	runWorkspaceTestGit(t, ws.WorktreePath, "add", "staged.txt")
	require.NoError(os.RemoveAll(ws.WorktreePath))
	tmux := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(tmux, []byte("#!/bin/sh\necho 'cannot connect to server' >&2\nexit 1\n"), 0o755))
	mgr.SetTmuxCommand([]string{tmux})
	require.Error(mgr.Setup(ctx, ws))
	assert.Equal(before, runWorkspaceTestGit(t, ws.WorktreePath, "rev-parse", "HEAD"))
	assert.Contains(string(runWorkspaceTestGit(t, ws.WorktreePath, "diff", "--cached", "--name-only")), "staged.txt")
	current, err := worktreeCurrentBranch(ctx, ws.WorktreePath)
	require.NoError(err)
	assert.Empty(current)
	workingTmux, _ := writeRecorderScript(t)
	mgr.SetTmuxCommand([]string{workingTmux})
	require.NoError(mgr.Setup(ctx, ws))
	assert.Equal(before, runWorkspaceTestGit(t, ws.WorktreePath, "rev-parse", "HEAD"))
	assert.Contains(string(runWorkspaceTestGit(t, ws.WorktreePath, "diff", "--cached", "--name-only")), "staged.txt")
}
