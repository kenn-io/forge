//go:build integration

package docs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
)

// remoteCommit advances the bare fixture remote by one commit created in a
// scratch clone, returning the new remote head SHA. This simulates another
// machine pushing docs changes. The explicit checkout matters: under the
// isolated git env the bare remote's default HEAD is master, so a fresh
// clone would otherwise sit on an unborn branch.
func (g *gitRepo) remoteCommit(t *testing.T, rel, body string) string {
	t.Helper()
	clone := t.TempDir()
	runGit(t, g.dir, "clone", g.remote, clone)
	runGit(t, clone, "checkout", "main")
	runGit(t, clone, "config", "user.email", "middleman-fixture@example.invalid")
	runGit(t, clone, "config", "user.name", "Middleman Fixture")
	full := filepath.Join(clone, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	runGit(t, clone, "add", "--", rel)
	runGit(t, clone, "commit", "-m", "remote update")
	runGit(t, clone, "push", "origin", "main")
	return gitOutput(t, clone, "rev-parse", "HEAD")
}

func TestGitPullFastForwards(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	want := g.remoteCommit(t, "remote.md", "# remote\n")

	res, err := g.registry.GitPull(context.Background(), g.folderID)

	require.NoError(err)
	assert.False(res.UpToDate)
	assert.Equal(want, res.Commit)
	assert.Equal(want[:7], res.ShortCommit)
	assert.Equal("main", res.Branch)
	assert.Equal("origin/main", res.Upstream)
	assert.Equal(want, gitOutput(t, g.dir, "rev-parse", "HEAD"))
	// The source-only refspec fetch must still refresh the remote-tracking
	// ref via git's opportunistic update, or origin/main would go stale and
	// the branch would wrongly appear ahead of its upstream.
	assert.Equal(want, gitOutput(t, g.dir, "rev-parse", "origin/main"))
	body, readErr := os.ReadFile(filepath.Join(g.dir, "remote.md"))
	require.NoError(readErr)
	assert.Equal("# remote\n", string(body))
}

func TestGitPullUpToDate(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	head := gitOutput(t, g.dir, "rev-parse", "HEAD")

	res, err := g.registry.GitPull(context.Background(), g.folderID)

	require.NoError(err)
	assert.True(res.UpToDate)
	assert.Equal(head, res.Commit)
	assert.Equal(head[:7], res.ShortCommit)
}

func TestGitPullRefusesDiverged(t *testing.T) {
	g := newGitRepo(t)
	g.remoteCommit(t, "remote.md", "remote\n")
	g.writeFile(t, "local.md", "local\n")
	runGit(t, g.dir, "add", "--", "local.md")
	runGit(t, g.dir, "commit", "-m", "local update")

	_, err := g.registry.GitPull(context.Background(), g.folderID)

	require.ErrorIs(t, err, ErrDiverged)
}

func TestGitPullRefusesOverwritingDirtyWorktree(t *testing.T) {
	g := newGitRepo(t)
	g.remoteCommit(t, "seed.md", "remote seed\n")
	g.writeFile(t, "seed.md", "local dirty\n")

	_, err := g.registry.GitPull(context.Background(), g.folderID)

	var pullFailed *PullFailedError
	require.ErrorAs(t, err, &pullFailed)
	assert.Contains(t, pullFailed.Stderr, "overwritten")
	// The dirty local edit must survive the refused pull.
	body, readErr := os.ReadFile(filepath.Join(g.dir, "seed.md"))
	require.NoError(t, readErr)
	assert.Equal(t, "local dirty\n", string(body))
}

func TestGitPullRefusesNoUpstream(t *testing.T) {
	g := newGitRepoNoUpstream(t)

	_, err := g.registry.GitPull(context.Background(), g.folderID)

	var noUpstream *NoUpstreamError
	require.ErrorAs(t, err, &noUpstream)
	assert.Contains(t, noUpstream.SuggestedCommand, "--set-upstream-to")
}

func TestGitPullRefusesNotARepo(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry([]config.DocFolder{{ID: "f", Name: "F", Path: dir}})

	_, err := reg.GitPull(context.Background(), "f")

	require.ErrorIs(t, err, ErrNotAGitRepo)
}
