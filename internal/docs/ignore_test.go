package docs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
)

// seedFolder writes each entry of files to disk under a fresh tempdir
// and returns a registry rooted at it. Keys are forward-slashed
// relative paths; values are file bodies.
func seedFolder(t *testing.T, files map[string]string) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		req := require.New(t)
		full := filepath.Join(root, filepath.FromSlash(rel))
		req.NoError(os.MkdirAll(filepath.Dir(full), 0o755))
		req.NoError(os.WriteFile(full, []byte(body), 0o644))
	}
	return NewRegistry([]config.DocFolder{
		{ID: "notes", Name: "Notes", Path: root},
	}), root
}

func TestBaselineHidesVCSAndCachesWithoutUserConfig(t *testing.T) {
	assert := Assert.New(t)
	// No .gitignore / .middlemanignore - baseline alone must still
	// drop .git, node_modules, and .DS_Store.
	r, _ := seedFolder(t, map[string]string{
		"kept.md":             "# Kept",
		".git/HEAD":           "ref",
		"node_modules/foo.md": "# nope",
		".DS_Store":           "blob",
		"deep/.git/config":    "ref",
	})
	tree, err := r.Tree("notes")
	require.NoError(t, err)
	names := walkNames(tree)
	assert.Contains(names, "kept.md")
	for _, banned := range []string{".git/HEAD", "node_modules/foo.md", ".DS_Store", "deep/.git/config"} {
		assert.NotContains(names, banned, "baseline should hide %q", banned)
	}
}

func TestRootGitignoreHidesPytestCache(t *testing.T) {
	assert := Assert.New(t)
	// The user's specific motivating case: a Python project mixing
	// notes with code, where .pytest_cache pollutes the docs view.
	r, _ := seedFolder(t, map[string]string{
		".gitignore":               ".pytest_cache/\n",
		"kept.md":                  "# Kept",
		".pytest_cache/v/cache.md": "# nope",
	})
	tree, err := r.Tree("notes")
	require.NoError(t, err)
	names := walkNames(tree)
	assert.Contains(names, "kept.md")
	assert.NotContains(names, ".pytest_cache/v/cache.md", ".pytest_cache should be hidden")
}

func TestNestedGitignoreScopesToItsSubtree(t *testing.T) {
	assert := Assert.New(t)
	// A nested .gitignore must hide files only under its own dir,
	// not its siblings.
	r, _ := seedFolder(t, map[string]string{
		"notes/.gitignore": "secret.md\n",
		"notes/kept.md":    "# kept",
		"notes/secret.md":  "shh",
		"other/secret.md":  "still here",
	})
	tree, err := r.Tree("notes")
	require.NoError(t, err)
	names := walkNames(tree)
	assert.Contains(names, "notes/kept.md")
	assert.NotContains(names, "notes/secret.md", "nested .gitignore should hide subtree files")
	assert.Contains(names, "other/secret.md", "nested .gitignore should not affect sibling dirs")
}

func TestMiddlemanIgnoreOverlaysOnGitignore(t *testing.T) {
	assert := Assert.New(t)
	// .middlemanignore lives next to .gitignore at the root and
	// stacks additively - both layers can hide paths.
	r, _ := seedFolder(t, map[string]string{
		".gitignore":       "logs/\n",
		".middlemanignore": "drafts/\n",
		"kept.md":          "# Kept",
		"drafts/wip.md":    "# wip",
		"logs/build.md":    "# build",
	})
	tree, err := r.Tree("notes")
	require.NoError(t, err)
	names := walkNames(tree)
	assert.Contains(names, "kept.md")
	assert.NotContains(names, "drafts/wip.md", "drafts/ should be hidden by .middlemanignore")
	assert.NotContains(names, "logs/build.md", "logs/ should be hidden by .gitignore")
}

func TestMissingIgnoreFilesAreSilent(t *testing.T) {
	// Folder with no .gitignore and no .middlemanignore should not
	// error - baseline applies and everything else stays visible.
	r, _ := seedFolder(t, map[string]string{
		"kept.md": "# Kept",
	})
	tree, err := r.Tree("notes")
	require.NoError(t, err, "tree without ignore files errored")
	Assert.Contains(t, walkNames(tree), "kept.md", "expected kept.md visible without ignore files")
}

func TestReadFileRefusesGitignoredPath(t *testing.T) {
	r, _ := seedFolder(t, map[string]string{
		".gitignore":    "drafts/\n",
		"drafts/wip.md": "# wip",
		"kept.md":       "# kept",
	})
	if _, err := r.ReadFile("notes", "drafts/wip.md"); !errors.Is(err, ErrOutsideFolder) {
		Assert.ErrorIs(t, err, ErrOutsideFolder)
	}
	// Non-ignored read still works.
	body, err := r.ReadFile("notes", "kept.md")
	require.NoError(t, err, "kept.md should read")
	Assert.Equal(t, "# kept", string(body))
}

func TestWriteFileRefusesGitignoredPath(t *testing.T) {
	r, _ := seedFolder(t, map[string]string{
		".gitignore":    "drafts/\n",
		"drafts/wip.md": "old",
	})
	if err := r.WriteFile("notes", "drafts/wip.md", []byte("new")); !errors.Is(err, ErrOutsideFolder) {
		Assert.ErrorIs(t, err, ErrOutsideFolder)
	}
}

func TestCreateFileRefusesGitignoredPath(t *testing.T) {
	r, _ := seedFolder(t, map[string]string{
		".gitignore":    "drafts/\n",
		"drafts/wip.md": "x",
	})
	if err := r.CreateFile("notes", "drafts/new.md", []byte("# new")); !errors.Is(err, ErrOutsideFolder) {
		Assert.ErrorIs(t, err, ErrOutsideFolder)
	}
}

func TestDeleteFileRefusesGitignoredPath(t *testing.T) {
	r, _ := seedFolder(t, map[string]string{
		".gitignore":    "drafts/\n",
		"drafts/wip.md": "x",
	})
	if err := r.DeleteFile("notes", "drafts/wip.md"); !errors.Is(err, ErrOutsideFolder) {
		Assert.ErrorIs(t, err, ErrOutsideFolder)
	}
}

func TestRenameFileRefusesIgnoredSourceOrDest(t *testing.T) {
	r, _ := seedFolder(t, map[string]string{
		".gitignore":    "drafts/\n",
		"kept.md":       "# kept",
		"drafts/wip.md": "wip",
	})
	if err := r.RenameFile("notes", "drafts/wip.md", "moved.md"); !errors.Is(err, ErrOutsideFolder) {
		Assert.ErrorIs(t, err, ErrOutsideFolder)
	}
	if err := r.RenameFile("notes", "kept.md", "drafts/kept.md"); !errors.Is(err, ErrOutsideFolder) {
		Assert.ErrorIs(t, err, ErrOutsideFolder)
	}
}

func TestReadBlobRefusesGitignoredPath(t *testing.T) {
	req := require.New(t)
	r, root := seedFolder(t, map[string]string{
		".gitignore": "drafts/\n",
	})
	pngData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	req.NoError(os.MkdirAll(filepath.Join(root, "drafts"), 0o755))
	req.NoError(os.WriteFile(filepath.Join(root, "drafts/icon.png"), pngData, 0o644))
	if _, err := r.ReadBlob("notes", "drafts/icon.png"); !errors.Is(err, ErrOutsideFolder) {
		Assert.ErrorIs(t, err, ErrOutsideFolder)
	}
}

func TestReadFileRefusesMiddlemanIgnoredPath(t *testing.T) {
	r, _ := seedFolder(t, map[string]string{
		".middlemanignore": "private/\n",
		"private/diary.md": "personal",
	})
	if _, err := r.ReadFile("notes", "private/diary.md"); !errors.Is(err, ErrOutsideFolder) {
		Assert.ErrorIs(t, err, ErrOutsideFolder)
	}
}

func TestTreeDoesNotDescendIntoIgnoredDir(t *testing.T) {
	// A subtree that's wholly ignored shouldn't even be enumerated -
	// the tree walk skips the directory entirely.
	r, _ := seedFolder(t, map[string]string{
		".gitignore":           "build/\n",
		"build/deep/nested.md": "# nope",
		"kept.md":              "# kept",
	})
	tree, err := r.Tree("notes")
	require.NoError(t, err)
	for _, child := range tree.Children {
		if child.IsDir && child.Name == "build" {
			Assert.False(t, child.IsDir && child.Name == "build", "build/ should be entirely absent from tree")
		}
	}
}

func TestBaselineSurvivesUserGitignore(t *testing.T) {
	// Even if a user's .gitignore is empty, baseline still applies.
	r, _ := seedFolder(t, map[string]string{
		".gitignore": "",
		"kept.md":    "# kept",
		".git/HEAD":  "ref",
	})
	tree, err := r.Tree("notes")
	require.NoError(t, err)
	names := walkNames(tree)
	assert := Assert.New(t)
	assert.Contains(names, "kept.md")
	assert.NotContains(names, ".git/HEAD", "baseline must apply even with empty .gitignore")
}
