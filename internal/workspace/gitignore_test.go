package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureGeneratedContextFilesIgnoredAppendsMissingEntriesToGitExclude(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	worktree := initWorkspaceGitRepo(t)

	require.NoError(EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{
		".middleman/agent-context.md",
		"AGENTS.local.md",
		"CLAUDE.local.md",
	}))

	excludeText := readGitExclude(t, worktree)
	assert.Contains(excludeText, "# middleman generated agent context")
	assert.Contains(excludeText, "/.middleman/")
	assert.Contains(excludeText, "/AGENTS.local.md")
	assert.Contains(excludeText, "/CLAUDE.local.md")
	assert.NotContains(excludeText, "/CLAUDE.md")
	assert.NotContains(excludeText, "/AGENTS.md")
	assertGitIgnored(t, worktree, ".middleman/agent-context.md")
	assertGitIgnored(t, worktree, "AGENTS.local.md")
	assertGitIgnored(t, worktree, "CLAUDE.local.md")
}

func TestEnsureGeneratedContextFilesIgnoredLeavesExistingIgnoresAlone(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	worktree := initWorkspaceGitRepo(t)
	initial := "dist/\n/.middleman/\n/AGENTS.local.md\n/CLAUDE.local.md\n"
	writeGitExclude(t, worktree, initial)

	require.NoError(EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{
		".middleman/agent-context.md",
		"AGENTS.local.md",
		"CLAUDE.local.md",
	}))

	assert.Equal(t, initial, readGitExclude(t, worktree))
}

func TestGeneratedContextFilesDoNotDirtyGitStatus(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	worktree := initWorkspaceGitRepo(t)

	require.NoError(EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{
		".middleman/agent-context.md",
		"AGENTS.local.md",
	}))
	require.NoError(os.MkdirAll(filepath.Join(worktree, ".middleman"), 0o755))
	require.NoError(os.WriteFile(filepath.Join(worktree, ".middleman", "agent-context.md"), []byte("context\n"), 0o644))
	require.NoError(os.WriteFile(filepath.Join(worktree, "AGENTS.local.md"), []byte("context\n"), 0o644))

	status := strings.TrimSpace(string(runWorkspaceTestGit(t, worktree, "status", "--porcelain")))
	assert.Empty(t, status)
}

func TestEnsureGeneratedContextFilesIgnoredRejectsRootInstructionFiles(t *testing.T) {
	t.Parallel()
	worktree := initWorkspaceGitRepo(t)

	err := EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{"CLAUDE.md"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to add root instruction file")
}

func initWorkspaceGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initWorkspaceGitRepoAt(t, dir)
	return dir
}

func initWorkspaceGitRepoAt(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	runWorkspaceTestGit(t, dir, "init", "--initial-branch=main")
	runWorkspaceTestGit(t, dir, "config", "user.email", "test@example.test")
	runWorkspaceTestGit(t, dir, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644))
	runWorkspaceTestGit(t, dir, "add", "README.md")
	runWorkspaceTestGit(t, dir, "commit", "-m", "initial")
}

func assertGitIgnored(t *testing.T, dir, rel string) {
	t.Helper()
	runWorkspaceTestGit(t, dir, "check-ignore", "--quiet", "--", rel)
}

func readGitExclude(t *testing.T, dir string) string {
	t.Helper()
	path := gitExcludePath(t, dir)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(content)
}

func writeGitExclude(t *testing.T, dir, content string) {
	t.Helper()
	path := gitExcludePath(t, dir)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func gitExcludePath(t *testing.T, dir string) string {
	t.Helper()
	out := strings.TrimSpace(string(runWorkspaceTestGit(t, dir, "rev-parse", "--git-path", "info/exclude")))
	if filepath.IsAbs(out) {
		return out
	}
	return filepath.Join(dir, out)
}
