package gitclone

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCommitStats(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	commitTestRun(t, dir, "git", "init", "--initial-branch=main")
	commitTestRun(t, dir, "git", "config", "user.name", "Alice")
	commitTestRun(t, dir, "git", "config", "user.email", "alice@example.test")
	write := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	var commits []Commit
	expected := make(map[string]*CommitStats)
	commit := func(message string, additions, deletions int) string {
		commitTestRun(t, dir, "git", "add", ".")
		commitTestRun(t, dir, "git", "commit", "--allow-empty", "-m", message)
		sha := gitSHA(t, dir, "HEAD")
		commits = append(commits, Commit{SHA: sha})
		expected[sha] = &CommitStats{Additions: additions, Deletions: deletions}
		return sha
	}
	write("file.txt", "one\ntwo\nthree\n")
	root := commit("root", 3, 0)
	write("file.txt", "one\nchanged\nthree\n")
	commit("edit", 1, 1)
	commitTestRun(t, dir, "git", "mv", "file.txt", root)
	commit("rename to SHA-like path", 0, 0)
	commitTestRun(t, dir, "git", "mv", root, "tab\tand\nnewline.txt")
	commit("rename unusual path", 0, 0)
	write("binary.dat", "\x00binary\x00\n")
	commit("binary", 0, 0)
	commit("empty", 0, 0)
	commitTestRun(t, dir, "git", "checkout", "-b", "side")
	write("side.txt", "side one\nside two\n")
	commit("side", 2, 0)
	commitTestRun(t, dir, "git", "checkout", "main")
	write("main.txt", "main\n")
	commit("main", 1, 0)
	commitTestRun(t, dir, "git", "merge", "--no-ff", "side", "-m", "merge")
	mergeSHA := gitSHA(t, dir, "HEAD")
	commits = append(commits, Commit{SHA: mergeSHA})
	expected[mergeSHA] = &CommitStats{Additions: 2, Deletions: 0}
	stats, err := ReadCommitStats(t.Context(), dir, commits)
	require.NoError(t, err)
	assert.Equal(t, expected, stats)
}

func TestReadCommitStatsNoCommits(t *testing.T) {
	t.Parallel()
	stats, err := ReadCommitStats(t.Context(), t.TempDir(), nil)
	require.NoError(t, err)
	assert.Empty(t, stats)
}
