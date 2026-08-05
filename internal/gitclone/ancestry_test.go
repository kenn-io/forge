package gitclone

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAncestryClone(t *testing.T) (*Manager, map[string]string) {
	t.Helper()

	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "source")
	commitTestRun(t, dir, "git", "init", "--initial-branch=main", sourceDir)
	commitTestRun(t, sourceDir, "git", "config", "user.email", "alice@test.com")
	commitTestRun(t, sourceDir, "git", "config", "user.name", "Alice")

	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, sourceDir, "git", "add", ".")
	commitTestRun(t, sourceDir, "git", "commit", "-m", "c1")
	c1 := gitSHA(t, sourceDir, "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "main.txt"), []byte("main\n"), 0o644))
	commitTestRun(t, sourceDir, "git", "add", ".")
	commitTestRun(t, sourceDir, "git", "commit", "-m", "c2")
	c2 := gitSHA(t, sourceDir, "HEAD")

	commitTestRun(t, sourceDir, "git", "checkout", "-b", "side", c1)
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "side.txt"), []byte("side\n"), 0o644))
	commitTestRun(t, sourceDir, "git", "add", ".")
	commitTestRun(t, sourceDir, "git", "commit", "-m", "c3")
	c3 := gitSHA(t, sourceDir, "HEAD")

	mgr := New(filepath.Join(dir, "clones"), nil)
	clonePath, err := mgr.ClonePath("github", "example.com", "acme", "widgets")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(clonePath), 0o755))
	commitTestRun(t, dir, "git", "clone", "--bare", sourceDir, clonePath)

	return mgr, map[string]string{"c1": c1, "c2": c2, "c3": c3}
}

func TestIsAncestor(t *testing.T) {
	ctx := context.Background()
	mgr, shas := setupAncestryClone(t)
	assert := assert.New(t)

	ancestor, err := mgr.IsAncestor(ctx, "github", "example.com", "acme", "widgets", shas["c1"], shas["c2"])
	require.NoError(t, err)
	assert.True(ancestor)

	ancestor, err = mgr.IsAncestor(ctx, "github", "example.com", "acme", "widgets", shas["c2"], shas["c1"])
	require.NoError(t, err)
	assert.False(ancestor)

	ancestor, err = mgr.IsAncestor(ctx, "github", "example.com", "acme", "widgets", shas["c3"], shas["c2"])
	require.NoError(t, err)
	assert.False(ancestor)
}

func TestHasCommit(t *testing.T) {
	ctx := context.Background()
	mgr, shas := setupAncestryClone(t)
	assert := assert.New(t)

	has, err := mgr.HasCommit(ctx, "github", "example.com", "acme", "widgets", shas["c1"])
	require.NoError(t, err)
	assert.True(has)

	has, err = mgr.HasCommit(ctx, "github", "example.com", "acme", "widgets", strings.Repeat("d", 40))
	require.NoError(t, err)
	assert.False(has)
}
