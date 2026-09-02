package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func setupDivergenceWorktree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runWorkspaceTestGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	runWorkspaceTestGit(t, root, "clone", remote, work)
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "base")
	runWorkspaceTestGit(t, work, "push", "origin", "main")
	runWorkspaceTestGit(t, work, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(work, "f.txt"), []byte("f1\n"), 0o644))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "feature 1")
	runWorkspaceTestGit(t, work, "push", "-u", "origin", "feature")
	return work
}
