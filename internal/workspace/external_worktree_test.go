package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
)

func widgetIdentity(remote string) *db.PlatformIdentity {
	if !strings.Contains(remote, "acme/widget") {
		return nil
	}
	return &db.PlatformIdentity{Platform: "github", Host: "github.com", Owner: "acme", Name: "widget"}
}

func commitInWorktree(t *testing.T, worktree, name string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(worktree, name), []byte(name+"\n"), 0o644))
	runWorkspaceTestGit(t, worktree, "add", ".")
	runWorkspaceTestGit(t, worktree, "commit", "-m", name)
	return strings.TrimSpace(string(runWorkspaceTestGit(t, worktree, "rev-parse", "HEAD")))
}

func TestFindExternalWorktreePR(t *testing.T) {
	setup := func(t *testing.T) (string, string) {
		t.Helper()
		worktree := setupMonitorRepo(t)
		runWorkspaceTestGit(t, worktree, "checkout", "-b", "feature/login")
		head := commitInWorktree(t, worktree, "login.txt")
		runWorkspaceTestGit(t, worktree, "push", "-u", "origin", "feature/login")
		runWorkspaceTestGit(t, worktree, "remote", "set-url", "origin", "git@github.com:acme/widget.git")
		return worktree, head
	}
	find := func(t *testing.T, cwd string, candidates ...db.MergeRequest) *ExternalWorktreePR {
		t.Helper()
		monitor := NewPRMonitor(openTestDB(t), PRMonitorOptions{
			PullCandidates: &staticPullCandidateSource{candidates: candidates},
		})
		match, err := monitor.FindExternalWorktreePR(t.Context(), cwd, widgetIdentity)
		require.NoError(t, err)
		return match
	}
	pull := func(head string) db.MergeRequest {
		return db.MergeRequest{
			Number: 42, State: db.MergeRequestStateOpen, HeadBranch: "feature/login",
			HeadRepoCloneURL: "https://github.com/acme/widget.git", PlatformHeadSHA: head,
		}
	}

	t.Run("a worktree holding the pull request's head matches", func(t *testing.T) {
		worktree, head := setup(t)
		root, err := filepath.EvalSymlinks(worktree)
		require.NoError(t, err)
		match := find(t, worktree, pull(head))
		require.NotNil(t, match)
		assert.Equal(t, 42, match.Number)
		assert.Equal(t, root, match.WorktreePath)
		assert.Equal(t, "feature/login", match.Branch)
		assert.Equal(t, "acme", match.Identity.Owner)
	})
	t.Run("an agent working in a subdirectory matches its worktree", func(t *testing.T) {
		worktree, head := setup(t)
		sub := filepath.Join(worktree, "nested")
		require.NoError(t, os.Mkdir(sub, 0o755))
		root, err := filepath.EvalSymlinks(worktree)
		require.NoError(t, err)
		match := find(t, sub, pull(head))
		require.NotNil(t, match)
		assert.Equal(t, root, match.WorktreePath)
	})
	t.Run("commits past the pushed head still match", func(t *testing.T) {
		worktree, head := setup(t)
		commitInWorktree(t, worktree, "more.txt")
		assert.NotNil(t, find(t, worktree, pull(head)))
	})
	t.Run("a reused branch name without the head commit does not match", func(t *testing.T) {
		worktree, _ := setup(t)
		assert.Nil(t, find(t, worktree, pull(strings.Repeat("a", 40))))
	})
	t.Run("a branch with no open pull request does not match", func(t *testing.T) {
		worktree, head := setup(t)
		other := pull(head)
		other.HeadBranch = "feature/other"
		assert.Nil(t, find(t, worktree, other))
	})
	t.Run("a detached checkout does not match", func(t *testing.T) {
		worktree, head := setup(t)
		runWorkspaceTestGit(t, worktree, "checkout", "--detach")
		assert.Nil(t, find(t, worktree, pull(head)))
	})
	t.Run("a remote for another repository does not match", func(t *testing.T) {
		worktree, head := setup(t)
		runWorkspaceTestGit(t, worktree, "remote", "set-url", "origin", "git@github.com:other/project.git")
		assert.Nil(t, find(t, worktree, pull(head)))
	})
	t.Run("a directory outside any checkout does not match", func(t *testing.T) {
		assert.Nil(t, find(t, t.TempDir(), pull("")))
	})
}

func externalWorkspaceFixture(t *testing.T) (*Manager, *db.DB, *Workspace, string) {
	t.Helper()
	database := openTestDB(t)
	worktree := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktree, "checkout", "-b", "feature/seven")
	commitInWorktree(t, worktree, "seven.txt")
	root, err := filepath.EvalSymlinks(worktree)
	require.NoError(t, err)
	manager := newTestManager(t, database, t.TempDir())
	tmuxScript, _ := writeRecorderScript(t)
	manager.SetTmuxCommand([]string{tmuxScript})
	spec := launchSpecForTest()
	spec.IssuedAt = time.Now().UTC()
	spec.SourceVisibleUntil = spec.IssuedAt.Add(WorkspaceLaunchSpecVisibilityLease)
	seedLaunchSpecRepository(t, database, spec)
	ws, err := manager.CreateExternalFromLaunchSpec(t.Context(), spec, root, "feature/seven")
	require.NoError(t, err)
	return manager, database, ws, root
}

func TestSetupOnExternalWorktreeLeavesTheCheckoutAlone(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	manager, database, ws, root := externalWorkspaceFixture(t)
	require.NoError(os.WriteFile(filepath.Join(root, "unsaved.txt"), []byte("wip\n"), 0o644))
	head := runWorkspaceTestGit(t, root, "rev-parse", "HEAD")
	config := runWorkspaceTestGit(t, root, "config", "--list", "--local")

	require.NoError(manager.Setup(t.Context(), ws))

	stored, err := database.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	assert.Equal("ready", stored.Status)
	assert.True(stored.ExternalWorktree)
	assert.Equal(root, stored.WorktreePath)
	assert.Equal("feature/seven", stored.WorkspaceBranch)
	assert.FileExists(filepath.Join(root, "unsaved.txt"))
	assert.Equal(head, runWorkspaceTestGit(t, root, "rev-parse", "HEAD"))
	assert.Equal(string(config), string(runWorkspaceTestGit(t, root, "config", "--list", "--local")), "setup configured the external checkout")
}

func TestSetupOnExternalWorktreeFailsWhenItMovedBranch(t *testing.T) {
	manager, database, ws, root := externalWorkspaceFixture(t)
	runWorkspaceTestGit(t, root, "checkout", "-b", "feature/other")

	require.ErrorContains(t, manager.Setup(t.Context(), ws), "feature/other")

	stored, err := database.GetWorkspace(t.Context(), ws.ID)
	require.NoError(t, err)
	assert.Equal(t, "error", stored.Status)
}

func TestDeleteLeavesAnExternalWorktreeAndBranch(t *testing.T) {
	for name, force := range map[string]bool{"without force": false, "with force": true} {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			manager, database, ws, root := externalWorkspaceFixture(t)
			require.NoError(manager.Setup(t.Context(), ws))
			require.NoError(os.WriteFile(filepath.Join(root, "unsaved.txt"), []byte("wip\n"), 0o644))

			dirty, err := manager.Delete(t.Context(), ws.ID, force, nil)

			require.NoError(err)
			assert.Empty(dirty, "uncommitted work blocks nothing, since none is removed")
			assert.FileExists(filepath.Join(root, "unsaved.txt"))
			assert.FileExists(filepath.Join(root, "seven.txt"))
			assert.Equal("feature/seven\n", string(runWorkspaceTestGit(t, root, "branch", "--show-current")))
			stored, err := database.GetWorkspace(t.Context(), ws.ID)
			require.NoError(err)
			assert.Nil(stored)
		})
	}
}
