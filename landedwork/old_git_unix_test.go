//go:build unix

package landedwork_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

// olderGit puts a git first on PATH that rejects one option the way an older
// release does: unknown options exit 129 with a usage message. Every other
// invocation runs the real Git.
func olderGit(t *testing.T, option string) {
	t.Helper()
	git, err := exec.LookPath("git")
	require.NoError(t, err)
	dir := t.TempDir()
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n  case \"$arg\" in " + option + "*)\n" +
		"    echo \"error: unknown option \\`${arg#--}'\" >&2; exit 129;;\n  esac\ndone\n" +
		"exec " + shellQuote(git) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// repeatedTextAcrossBases lands a source edit whose removed text repeats, on a
// target whose base moved. Only an exact three-way merge from the source's own
// base can locate the edit, so the proof needs Git's merge-tree.
func repeatedTextAcrossBases(t *testing.T) fixture {
	t.Helper()
	f := buildFixture(t, true)
	f.repo.Checkout("-b", "common", f.base)
	common := f.repo.CommitFile("text", "header\npadding\nold\nkeep\nold\nkeep\nend\n", "repeated text")
	f.repo.Checkout("-b", "source", common)
	content := "header\npadding\nnew\nkeep\nold\nkeep\nend\n"
	f.source = []string{f.repo.CommitFile("text", content, "source")}
	f.repo.Checkout("-b", "target", common)
	f.base = f.repo.CommitFile("text", "prefix\nheader\npadding\nold\nkeep\nold\nkeep\nend\n", "shift base")
	f.head = f.repo.CommitFile("text", "prefix\n"+content, "land")
	return f
}

// Git 2.38 and 2.39, as shipped by Debian 12 and macOS, have
// merge-tree --write-tree but not --merge-base (added in 2.40).
func TestRepeatedTextProofWithoutMergeBaseOption(t *testing.T) {
	f := repeatedTextAcrossBases(t)
	olderGit(t, "--merge-base")
	ctx, p := f.prepare(t)
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "squash"), fixtureLimits())
	require.NoError(t, err)
	require.Empty(t, r.Coverage.Gaps)
	require.Len(t, r.Landings, 1)
	assert.Equal(t, []string{f.head}, r.Landings[0].Spine)
	assert.True(t, r.Coverage.Complete)
}

// Before Git 2.38 merge-tree cannot write a merged tree. The gap names that
// limit instead of reporting missing objects.
func TestRepeatedTextProofReportsGitWithoutMergeTree(t *testing.T) {
	f := repeatedTextAcrossBases(t)
	olderGit(t, "--write-tree")
	ctx, p := f.prepare(t)
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "squash"), fixtureLimits())
	require.NoError(t, err)
	assert.Empty(t, r.Landings)
	assert.False(t, r.Coverage.Complete)
	require.Len(t, r.Coverage.Gaps, 1)
	assert.Equal(t, "merge_tree_unsupported", r.Coverage.Gaps[0].Reason)
}
