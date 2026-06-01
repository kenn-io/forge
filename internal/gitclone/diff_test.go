//go:build integration

package gitclone

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
)

func TestDiff(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// Create a test repo with two commits on different branches.
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")

	run(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)
	run(t, dir, "git", "clone", remote, work)
	run(t, work, "git", "config", "user.email", "test@test.com")
	run(t, work, "git", "config", "user.name", "Test")

	// Initial commit on main.
	require.NoError(os.WriteFile(filepath.Join(work, "hello.go"),
		[]byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"), 0o644))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "initial")
	run(t, work, "git", "push", "origin", "main")

	// Create a feature branch with changes.
	run(t, work, "git", "checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "hello.go"),
		[]byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello world\")\n}\n"), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "new.go"),
		[]byte("package main\n"), 0o644))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "feature changes")
	run(t, work, "git", "push", "origin", "feature")

	// Get SHAs.
	mainSHA := getSHA(t, work, "origin/main")
	featureSHA := getSHA(t, work, "origin/feature")

	// Clone into manager.
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)
	require.NoError(mgr.EnsureClone(
		t.Context(), "github.com", "test", "repo", remote))

	// Compute merge base.
	mb, err := mgr.MergeBase(
		t.Context(), "github.com", "test", "repo",
		mainSHA, featureSHA)
	require.NoError(err)
	assert.Equal(mainSHA, mb) // merge base is the initial commit

	// Run diff.
	result, err := mgr.Diff(
		t.Context(), "github.com", "test", "repo",
		mb, featureSHA, false)
	require.NoError(err)
	require.Len(result.Files, 2)

	// hello.go should be modified.
	assert.Equal("hello.go", result.Files[0].Path)
	assert.Equal("modified", result.Files[0].Status)
	assert.Equal(1, result.Files[0].Additions)
	assert.Equal(1, result.Files[0].Deletions)

	// new.go should be added.
	assert.Equal("new.go", result.Files[1].Path)
	assert.Equal("added", result.Files[1].Status)
}

func TestDiffFiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// Create a test repo with two commits on different branches.
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")

	run(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)
	run(t, dir, "git", "clone", remote, work)
	run(t, work, "git", "config", "user.email", "test@test.com")
	run(t, work, "git", "config", "user.name", "Test")

	// Initial commit on main.
	require.NoError(os.WriteFile(filepath.Join(work, "hello.go"),
		[]byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"), 0o644))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "initial")
	run(t, work, "git", "push", "origin", "main")

	// Create a feature branch with changes.
	run(t, work, "git", "checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "hello.go"),
		[]byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello world\")\n}\n"), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "new.go"),
		[]byte("package main\n"), 0o644))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "feature changes")
	run(t, work, "git", "push", "origin", "feature")

	mainSHA := getSHA(t, work, "origin/main")
	featureSHA := getSHA(t, work, "origin/feature")

	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)
	require.NoError(mgr.EnsureClone(
		t.Context(), "github.com", "test", "repo", remote))

	mb, err := mgr.MergeBase(
		t.Context(), "github.com", "test", "repo",
		mainSHA, featureSHA)
	require.NoError(err)

	files, err := mgr.DiffFiles(
		t.Context(), "github.com", "test", "repo",
		mb, featureSHA)
	require.NoError(err)
	require.Len(files, 2)

	// File metadata present.
	assert.Equal("hello.go", files[0].Path)
	assert.Equal("modified", files[0].Status)
	assert.Equal("new.go", files[1].Path)
	assert.Equal("added", files[1].Status)

	// No patch content.
	assert.Empty(files[0].Hunks)
	assert.Empty(files[1].Hunks)
	assert.Equal(1, files[0].Additions)
	assert.Equal(1, files[0].Deletions)
	assert.Equal(1, files[1].Additions)
	assert.Zero(files[1].Deletions)
}

func TestDiffFilesEmpty(t *testing.T) {
	assert := assert.New(t)

	// Diffing a commit against itself yields no files.
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")

	run(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)
	run(t, dir, "git", "clone", remote, work)
	run(t, work, "git", "config", "user.email", "test@test.com")
	run(t, work, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(work, "hello.go"),
		[]byte("package main\n"), 0o644))
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "initial")
	run(t, work, "git", "push", "origin", "main")

	sha := getSHA(t, work, "origin/main")
	clonesDir := t.TempDir()
	mgr := New(clonesDir, nil)
	require.NoError(t, mgr.EnsureClone(
		t.Context(), "github.com", "test", "repo", remote))

	// DiffFiles returns non-nil empty slice.
	files, err := mgr.DiffFiles(
		t.Context(), "github.com", "test", "repo", sha, sha)
	require.NoError(t, err)
	assert.NotNil(files)
	assert.Empty(files)

	// Diff returns non-nil empty slice.
	result, err := mgr.Diff(
		t.Context(), "github.com", "test", "repo", sha, sha, false)
	require.NoError(t, err)
	assert.NotNil(result.Files)
	assert.Empty(result.Files)
}

func TestDiffArgumentBuildersTerminateOptionsBeforeRevisions(t *testing.T) {
	assert := assert.New(t)

	assert.Contains(
		strings.Join(diffRawArgs("base", "head", false), " "),
		"--end-of-options base head",
	)
	assert.Contains(
		strings.Join(diffRawNoRenameArgs("base", "head", false), " "),
		"--end-of-options base head",
	)
}

func getSHA(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitcmd.New().Output(t.Context(), dir, "rev-parse", ref)
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}
