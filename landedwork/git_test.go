package landedwork_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

func TestMain(m *testing.M) { os.Exit(gitsafe.RunIsolatedMain(m)) }

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, stderr, err := gitsafe.Runner().Run(t.Context(), dir, nil, append([]string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
	require.NoError(t, err, "git %v: %s", args, stderr)
	return strings.TrimSpace(string(out))
}

func newHistory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--template=", "--initial-branch=main")
	gitRun(t, dir, "config", "user.name", "Author A")
	gitRun(t, dir, "config", "user.email", "author@example.org")
	return dir
}

func commitFiles(t *testing.T, dir, message string, files map[string]string) string {
	t.Helper()
	for name, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	gitRun(t, dir, "add", "--all")
	gitRun(t, dir, "commit", "-m", message)
	return gitRun(t, dir, "rev-parse", "HEAD")
}

func TestGitReadsPinnedObjectsAndAttributesWithoutLocalOverrides(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n", ".gitattributes": "*.go linguist-generated\n"})
	head := commitFiles(t, dir, "next", map[string]string{"main.go": "one\ntwo\n", ".gitattributes": "*.go -linguist-generated\n"})
	require.NoError(os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.go linguist-generated\n"), 0o600))
	require.NoError(os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o700))
	require.NoError(os.WriteFile(filepath.Join(dir, ".git", "info", "attributes"), []byte("*.go linguist-generated\n"), 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	meter, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
	require.NoError(err)
	source, err := landedwork.OpenGit(ctx, dir, meter)
	require.NoError(err)
	defer source.Close()
	commit, err := source.Commit(ctx, head, meter)
	require.NoError(err)
	assert.Equal([]string{base}, commit.Parents)
	assert.Equal([]byte("author@example.org"), commit.Author.Email)
	assert.Equal([]byte("next\n"), commit.Message)
	oldAttributes, err := source.Attributes(ctx, base, []string{"main.go"}, meter)
	require.NoError(err)
	newAttributes, err := source.Attributes(ctx, head, []string{"main.go"}, meter)
	require.NoError(err)
	assert.Equal(map[string]bool{"main.go": true}, oldAttributes)
	assert.Equal(map[string]bool{"main.go": false}, newAttributes)
	assert.Equal(head, gitRun(t, dir, "rev-parse", "HEAD"))
}

func TestGitReadRejectsMissingObjectsAndExhaustedBudget(t *testing.T) {
	require := require.New(t)
	dir := newHistory(t)
	head := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	budget := platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20}
	meter, err := platform.NewMeter(ctx, budget)
	require.NoError(err)
	source, err := landedwork.OpenGit(ctx, filepath.Join(dir, ".git"), meter)
	require.NoError(err)
	defer source.Close()
	_, err = source.Commit(ctx, strings.Repeat("1", 40), meter)
	require.Error(err)
	budget.MaxBytes = 8
	tiny, err := platform.NewMeter(ctx, budget)
	require.NoError(err)
	_, err = source.Commit(ctx, head, tiny)
	require.ErrorIs(err, platform.ErrPageLimit)
}

func TestGitDiffUsesExactRenamesAndKeepsBinarySeparate(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"a.go": "one\n"})
	gitRun(t, dir, "mv", "a.go", "b.go")
	renamed := commitFiles(t, dir, "rename", nil)
	gitRun(t, dir, "mv", "b.go", "c.go")
	edited := commitFiles(t, dir, "move and edit", map[string]string{"c.go": "one\ntwo\n", "image.bin": "\x00\xff\x01"})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	meter, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
	require.NoError(err)
	source, err := landedwork.OpenGit(ctx, dir, meter)
	require.NoError(err)
	defer source.Close()
	exact, err := source.Diff(ctx, base, renamed, meter)
	require.NoError(err)
	require.Len(exact.Files, 1)
	assert.True(exact.Files[0].Renamed)
	assert.Equal(new(int64(0)), exact.Files[0].Additions)
	assert.Equal(new(int64(0)), exact.Files[0].Deletions)
	changed, err := source.Diff(ctx, renamed, edited, meter)
	require.NoError(err)
	require.Len(changed.Files, 3)
	assert.False(changed.Files[0].Renamed)
	assert.Equal(new(int64(1)), changed.Files[0].Deletions)
	assert.Equal(new(int64(2)), changed.Files[1].Additions)
	assert.True(changed.Files[2].Binary)
	assert.Nil(changed.Files[2].Additions)
	assert.Nil(changed.Files[2].Deletions)
}

func TestPatchCorrespondenceIgnoresOffsetsButNotEditedBytes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\ntwo\n"})
	gitRun(t, dir, "switch", "-c", "feature")
	sourceID := commitFiles(t, dir, "insert", map[string]string{"main.go": "one\ninsert\ntwo\n"})
	gitRun(t, dir, "switch", "main")
	changedBase := commitFiles(t, dir, "prefix", map[string]string{"main.go": "header\none\ntwo\n"})
	replayed := commitFiles(t, dir, "replay", map[string]string{"main.go": "header\none\ninsert\ntwo\n"})
	different := commitFiles(t, dir, "different", map[string]string{"main.go": "header\none\nINSERT\ntwo\n"})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	meter, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
	require.NoError(err)
	git, err := landedwork.OpenGit(ctx, dir, meter)
	require.NoError(err)
	defer git.Close()
	original, err := git.Patch(ctx, base, sourceID, meter)
	require.NoError(err)
	equivalent, err := git.Patch(ctx, changedBase, replayed, meter)
	require.NoError(err)
	changed, err := git.Patch(ctx, changedBase, different, meter)
	require.NoError(err)
	assert.Equal(original, equivalent)
	assert.NotEqual(original, changed)
	assert.False(original.Empty)
}
