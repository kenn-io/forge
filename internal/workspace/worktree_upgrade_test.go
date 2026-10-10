package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	managed "go.kenn.io/kit/git/managed"
	"go.kenn.io/kwt/worktree"
)

func TestDeleteRecordedSameRepositorySymlinkPreservesGitArtifacts(t *testing.T) {
	for _, tt := range []struct {
		name         string
		dirty, force bool
	}{
		{name: "clean"},
		{name: "dirty refusal", dirty: true},
		{name: "forced dirty", dirty: true, force: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			root := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/topic")
			path := filepath.Join(t.TempDir(), "target")
			runWorkspaceTestGit(t, root, "worktree", "add", path, "-b", "kenn-forge/pr-42", "HEAD")
			alias := filepath.Join(t.TempDir(), "recorded")
			if err := os.Symlink(path, alias); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if tt.dirty {
				require.NoError(os.WriteFile(filepath.Join(path, "notes.txt"), []byte("keep"), 0o600))
			}
			database := openTestDB(t)
			ws := &Workspace{
				ID: "ws-recorded-symlink", Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
				ItemNumber: 42, GitHeadRef: "feature/topic", WorkspaceBranch: "kenn-forge/pr-42",
				WorktreePath: alias, Status: "ready",
			}
			require.NoError(database.InsertWorkspace(t.Context(), ws))
			manager := newTestManager(t, database, t.TempDir())
			manager.SetWorktreeBasePathResolver(staticBaseResolver(root))
			registrations := string(runWorkspaceTestGit(t, root, "worktree", "list", "--porcelain"))
			refs := string(runWorkspaceTestGit(t, root, "show-ref"))
			admitted := false
			dirty, err := manager.Delete(t.Context(), ws.ID, tt.force, func(context.Context) error { admitted = true; return nil })
			require.NoError(err)
			stored, err := database.GetWorkspace(t.Context(), ws.ID)
			require.NoError(err)
			if tt.dirty && !tt.force {
				require.NotEmpty(dirty)
				require.False(admitted)
				require.NotNil(stored)
			} else {
				require.Empty(dirty)
				require.True(admitted)
				require.Nil(stored)
			}
			link, err := os.Readlink(alias)
			require.NoError(err)
			require.Equal(path, link)
			require.Equal(registrations, string(runWorkspaceTestGit(t, root, "worktree", "list", "--porcelain")))
			require.Equal(refs, string(runWorkspaceTestGit(t, root, "show-ref")))
			quarantines, err := filepath.Glob(alias + ".orphaned-*")
			require.NoError(err)
			require.Empty(quarantines)
		})
	}
}

func TestExistingWorkspaceBranchStatesSurviveDeletion(t *testing.T) {
	for _, tt := range []struct {
		name, branch, status     string
		keepCheckout, keepBranch bool
	}{
		{name: "owned", branch: "kenn-forge/issue-7", status: "ready"},
		{name: "borrowed", status: "ready", keepBranch: true},
		{name: "failed setup", branch: "__kenn_forge_unknown__", status: "error"},
		{name: "pending adoption", branch: "__kenn_forge_recovery_pending__..state", status: "error", keepCheckout: true, keepBranch: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			root := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/topic")
			path := filepath.Join(t.TempDir(), "issue-7")
			runWorkspaceTestGit(t, root, "worktree", "add", path, "-b", "kenn-forge/issue-7", "HEAD")
			registration := strings.TrimSpace(string(runWorkspaceTestGit(t, path, "rev-parse", "--absolute-git-dir")))
			// The existing on-disk format, independent of the new library's writer.
			require.NoError(os.WriteFile(filepath.Join(registration, "kenn-forge-workspace-id"), []byte("legacy-workspace\n"), 0o600))
			database := openTestDB(t)
			ws := &Workspace{
				ID: "legacy-workspace", Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeIssue,
				ItemNumber: 7, GitHeadRef: "kenn-forge/issue-7", WorkspaceBranch: tt.branch,
				WorktreePath: path, Status: tt.status,
			}
			require.NoError(database.InsertWorkspace(t.Context(), ws))
			manager := newTestManager(t, database, t.TempDir())
			manager.SetWorktreeBasePathResolver(staticBaseResolver(root))
			dirty, err := manager.Delete(t.Context(), ws.ID, true, nil)
			require.NoError(err)
			require.Empty(dirty)
			stored, err := database.GetWorkspace(t.Context(), ws.ID)
			require.NoError(err)
			require.Nil(stored)
			if tt.keepCheckout {
				require.DirExists(path)
			} else {
				require.NoDirExists(path)
			}
			branches := string(runWorkspaceTestGit(t, root, "branch", "--list", "kenn-forge/issue-7"))
			require.Equal(tt.keepBranch, strings.TrimSpace(branches) != "")
		})
	}
}

func addTestWorktree(m *Manager, ctx context.Context, dir workspaceGitDir, ws *Workspace, opts workspaceGitFetchOptions) (branch string, err error) {
	err = m.withRepoLockForGitDir(ctx, dir.path, func(scope *worktree.Scope) error {
		branch, err = m.addWorktreeLocked(ctx, scope, dir, ws, opts)
		return err
	})
	return branch, err
}

func addTestPreferredWorktree(m *Manager, ctx context.Context, dir workspaceGitDir, ws *Workspace) (branch string, err error) {
	err = m.withRepoLockForGitDir(ctx, dir.path, func(scope *worktree.Scope) error {
		branch, err = m.addPreferredWorktree(ctx, scope, dir, ws)
		return err
	})
	return branch, err
}

func refreshTestWorktree(m *Manager, ctx context.Context, dir, remote string, ws *Workspace, spec *WorkspaceLaunchSpec, managed bool) (fetched bool, err error) {
	err = m.withRepoLockForGitDir(ctx, dir, func(scope *worktree.Scope) error {
		fetched, err = m.refreshExistingWorkspaceWorktree(ctx, scope, dir, remote, ws, spec, managed)
		return err
	})
	return fetched, err
}

func syncTestBaseBranch(t *testing.T, ctx context.Context, dir, remote, _ string, branch string, managed bool) error {
	m := newTestManager(t, openTestDB(t), t.TempDir())
	return m.withRepoLockForGitDir(ctx, dir, func(scope *worktree.Scope) error {
		return scope.SyncBase(ctx, worktree.BaseSyncRequest{Branch: branch, SourceRef: remoteTrackingRef(remote, branch), BackupPrefix: "refs/kenn-forge/base-backups/", Managed: managed})
	})
}

func createTestWorkspace(m *Manager, ctx context.Context, dir string, ws *Workspace, opts managed.CreateWorktreeOptions) (result worktree.CreateResult, err error) {
	err = m.withRepoLockForGitDir(ctx, dir, func(scope *worktree.Scope) error {
		result, err = createWorkspaceCheckout(ctx, scope, ws, opts, nil)
		return err
	})
	return result, err
}

func writeTestWorkspaceIdentity(t *testing.T, ctx context.Context, gitDir string, ws *Workspace) error {
	t.Helper()
	manager := newTestManager(t, nil, t.TempDir())
	repo, err := manager.openRepositoryWorktrees(ctx, gitDir)
	if err != nil {
		return err
	}
	_, err = repo.EnsureIdentity(ctx, ws.WorktreePath, workspaceIdentity(ws))
	return err
}

func gitDirTracksWorktreePath(
	ctx context.Context, gitDir, worktreePath string,
) (bool, error) {
	if strings.TrimSpace(worktreePath) == "" {
		return false, nil
	}
	want, err := canonicalWorktreeListPath(worktreePath)
	if err != nil {
		return false, fmt.Errorf("resolve workspace path: %w", err)
	}
	out, err := gitCombinedOutput(
		ctx, gitDir, "worktree", "list", "--porcelain",
	)
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		got, err := canonicalWorktreeListPath(strings.TrimSpace(path))
		if err != nil {
			return false, fmt.Errorf("resolve tracked worktree path: %w", err)
		}
		if got == want {
			return true, nil
		}
	}
	return false, nil
}

func worktreeGitDir(ctx context.Context, path string) (string, error) {
	out, err := gitCombinedOutput(ctx, path, "rev-parse", "--path-format=absolute", "--git-dir")
	return strings.TrimSpace(out), err
}
