// Package gitfixture builds synthetic Git histories for tests.
package gitfixture

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	gitcmd "go.kenn.io/kit/git/cmd"
)

// DivergenceWorktree creates a local feature branch that tracks a bare remote.
func DivergenceWorktree(t *testing.T) string {
	t.Helper()
	require := require.New(t)

	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	Run(t, root, "init", "--bare", "--initial-branch=main", remote)
	Run(t, root, "clone", remote, work)
	Run(t, work, "config", "user.email", "t@test.com")
	Run(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	Run(t, work, "add", ".")
	Run(t, work, "commit", "-m", "base")
	Run(t, work, "push", "origin", "main")
	Run(t, work, "checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "f.txt"), []byte("f1\n"), 0o644))
	Run(t, work, "add", ".")
	Run(t, work, "commit", "-m", "feature 1")
	Run(t, work, "push", "-u", "origin", "feature")
	return work
}

// Run executes Git with a per-test isolated configuration.
func Run(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	runner := gitsafe.MutableRunner(t).WithConfig("init.defaultBranch", "main")
	out, stderr, err := runner.Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
	return out
}

// AppendFileCommits adds count commits that successively replace path on ref.
// It uses one fast-import process so boundary-size histories do not exhaust
// shared subprocess capacity when Go packages run concurrently.
func AppendFileCommits(t *testing.T, dir, ref, path string, count int) {
	t.Helper()
	require := require.New(t)
	runner := gitcmd.New().WithConfig("gc.auto", "0").WithConfig("maintenance.auto", "false")
	base, err := runner.Output(t.Context(), dir, "rev-parse", ref)
	require.NoError(err)

	var stream bytes.Buffer
	parent := strings.TrimSpace(string(base))
	mark := 1
	startedAt := time.Now().UTC().Unix()
	for i := range count {
		content := fmt.Appendf(nil, "%d\n", i)
		fmt.Fprintf(&stream, "blob\nmark :%d\ndata %d\n", mark, len(content))
		stream.Write(content)
		stream.WriteByte('\n')
		blobMark := mark
		mark++

		message := fmt.Sprintf("churn %03d", i)
		fmt.Fprintf(
			&stream,
			"commit refs/heads/%s\nmark :%d\nauthor Alice <alice@example.com> %d +0000\ncommitter Alice <alice@example.com> %d +0000\ndata %d\n%s\nfrom %s\nM 100644 :%d %s\n\n",
			ref,
			mark,
			startedAt+int64(i),
			startedAt+int64(i),
			len(message),
			message,
			parent,
			blobMark,
			path,
		)
		parent = fmt.Sprintf(":%d", mark)
		mark++
	}

	out, stderr, err := runner.Run(t.Context(), dir, &stream, "fast-import", "--quiet")
	require.NoError(err, "git fast-import failed: %s%s", out, stderr)
	out, stderr, err = runner.Run(t.Context(), dir, nil, "reset", "--hard", ref)
	require.NoError(err, "git reset failed: %s%s", out, stderr)
}
