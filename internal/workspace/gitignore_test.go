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
		"AGENTS.override.md",
		"CLAUDE.local.md",
	}))

	excludeText := readGitExclude(t, worktree)
	assert.Contains(excludeText, "# kenn-forge generated files")
	assert.Contains(excludeText, "/AGENTS.override.md")
	assert.Contains(excludeText, "/CLAUDE.local.md")
	assert.Contains(excludeText, "/.tmp-agent-context-*")
	assertGitIgnored(t, worktree, ".tmp-agent-context-x")
	assert.NotContains(excludeText, "/CLAUDE.md")
	assert.NotContains(excludeText, "/AGENTS.md")
	assertGitIgnored(t, worktree, "AGENTS.override.md")
	assertGitIgnored(t, worktree, "CLAUDE.local.md")
}

func TestEnsureGeneratedContextFilesIgnoredLeavesExistingIgnoresAlone(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	worktree := initWorkspaceGitRepo(t)
	initial := "dist/\n/AGENTS.override.md\n/CLAUDE.local.md\n/.tmp-agent-context-*\n"
	writeGitExclude(t, worktree, initial)

	require.NoError(EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{
		"AGENTS.override.md",
		"CLAUDE.local.md",
	}))

	assert.Equal(t, initial, readGitExclude(t, worktree))
}

func TestGeneratedContextFilesDoNotDirtyGitStatus(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	worktree := initWorkspaceGitRepo(t)

	require.NoError(EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{
		"AGENTS.override.md",
	}))
	require.NoError(os.WriteFile(filepath.Join(worktree, "AGENTS.override.md"), []byte("context\n"), 0o644))

	status := strings.TrimSpace(string(runWorkspaceTestGit(t, worktree, "status", "--porcelain")))
	assert.Empty(t, status)
}

func TestEnsureGeneratedContextFilesIgnoredOnlyIgnoresRequestedPaths(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	worktree := initWorkspaceGitRepo(t)

	require.NoError(EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{
		"AGENTS.override.md",
	}))

	excludeText := readGitExclude(t, worktree)
	assert.Contains(excludeText, "/AGENTS.override.md")
	assert.NotContains(excludeText, "/CLAUDE.local.md")
	assertGitIgnored(t, worktree, "AGENTS.override.md")
	assertGitNotIgnored(t, worktree, "CLAUDE.local.md")
}

func TestEnsureGeneratedContextFilesIgnoredFailsWhenNegationKeepsPathVisible(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	worktree := initWorkspaceGitRepo(t)
	require.NoError(os.WriteFile(
		filepath.Join(worktree, ".gitignore"),
		[]byte("!AGENTS.override.md\n"), 0o644,
	))
	runWorkspaceTestGit(t, worktree, "add", ".gitignore")
	runWorkspaceTestGit(t, worktree, "commit", "-m", "add negation")

	err := EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{"AGENTS.override.md"})
	require.Error(err)
	assert.Contains(t, err.Error(), "still not ignored")
}

func TestEnsureGeneratedContextFilesIgnoredFailsOnFatalGitError(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	notARepo := t.TempDir()

	err := EnsureGeneratedContextFilesIgnored(context.Background(), notARepo, []string{"AGENTS.override.md"})
	require.Error(err)
	assert.Contains(t, err.Error(), "resolve git exclude path")
	assert.NoFileExists(t, filepath.Join(notARepo, ".git", "info", "exclude"))
}

func TestEnsureGeneratedContextFilesIgnoredRejectsUnknownPaths(t *testing.T) {
	t.Parallel()
	worktree := initWorkspaceGitRepo(t)

	err := EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{"notes/scratch.md"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown generated path")
}

func TestEnsureGeneratedContextFilesIgnoredRejectsRootInstructionFiles(t *testing.T) {
	t.Parallel()
	worktree := initWorkspaceGitRepo(t)

	err := EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{"CLAUDE.md"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to add root instruction file")
}

func TestEnsureWorkspaceGeneratedPathsIgnoredAddsPastedImageDirectory(t *testing.T) {
	t.Parallel()
	worktree := initWorkspaceGitRepo(t)

	require.NoError(t, EnsureWorkspaceGeneratedPathsIgnored(
		context.Background(), worktree, []string{PastedImageDirectory},
	))

	excludeText := readGitExclude(t, worktree)
	assert.Contains(t, excludeText, "/.kenn-forge/pasted-images/")
	assert.NotContains(t, excludeText, generatedContextTempPattern)
	assertGitIgnored(t, worktree, ".kenn-forge/pasted-images/paste-1.png")
}

func TestEnsureWorkspaceGeneratedPathsIgnoredPinsTrackedRulesInPrivateExclude(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	worktree := initWorkspaceGitRepo(t)
	require.NoError(os.WriteFile(
		filepath.Join(worktree, ".gitignore"),
		[]byte("/.kenn-forge/pasted-images/\n"),
		0o644,
	))
	runWorkspaceTestGit(t, worktree, "add", ".gitignore")
	runWorkspaceTestGit(t, worktree, "commit", "-m", "ignore generated images")

	require.NoError(EnsureWorkspaceGeneratedPathsIgnored(
		context.Background(), worktree, []string{PastedImageDirectory},
	))

	assert.Contains(readGitExclude(t, worktree), "/.kenn-forge/pasted-images/")
	require.NoError(os.WriteFile(filepath.Join(worktree, ".gitignore"), nil, 0o644))
	assertGitIgnored(t, worktree, ".kenn-forge/pasted-images/paste-1.png")
}

func TestEnsureWorkspaceGeneratedPathsIgnoredRejectsUnknownDirectory(t *testing.T) {
	t.Parallel()
	worktree := initWorkspaceGitRepo(t)

	err := EnsureWorkspaceGeneratedPathsIgnored(
		context.Background(), worktree, []string{".kenn-forge/other"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown generated path")
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

func assertGitNotIgnored(t *testing.T, dir, rel string) {
	t.Helper()
	ignored, err := gitPathIgnored(context.Background(), dir, rel)
	require.NoError(t, err)
	assert.False(t, ignored, "expected %s to remain unignored", rel)
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
