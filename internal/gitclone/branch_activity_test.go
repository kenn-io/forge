package gitclone

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBranchActivityWalksDefaultBranchFirstParent(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@example.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")

	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "base")

	commitTestRun(t, work, "git", "checkout", "-b", "side")
	require.NoError(os.WriteFile(filepath.Join(work, "side1.txt"), []byte("side1\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "side commit 1")
	require.NoError(os.WriteFile(filepath.Join(work, "side2.txt"), []byte("side2\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "side commit 2")

	commitTestRun(t, work, "git", "checkout", "main")
	require.NoError(os.WriteFile(filepath.Join(work, "main.txt"), []byte("main\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "main work")
	commitTestRun(t, work, "git", "merge", "--no-ff", "side", "-m", "merge side")
	commitTestRun(t, work, "git", "push", "origin", "main")
	mergeSHA := gitSHA(t, work, "HEAD")

	mgr := New(filepath.Join(dir, "clones"), nil)
	require.NoError(mgr.EnsureClone(t.Context(), "github.com", "acme", "widgets", remote))

	commits, err := mgr.ListBranchCommitsSince(
		t.Context(), "github.com", "acme", "widgets", "main", time.Unix(0, 0).UTC(), "",
	)
	require.NoError(err)

	var subjects []string
	for _, commit := range commits {
		subjects = append(subjects, commit.Message)
	}
	assert.Equal([]string{"merge side", "main work", "base"}, subjects)
	assert.Equal(mergeSHA, commits[0].SHA)
	assert.Equal("Alice", commits[0].AuthorName)
	assert.Equal("alice@example.com", commits[0].AuthorEmail)
	assert.Equal("Alice", commits[0].CommitterName)
	assert.Equal("alice@example.com", commits[0].CommitterEmail)
	assert.False(commits[0].AuthoredAt.IsZero())
	assert.False(commits[0].CommittedAt.IsZero())
	assert.NotContains(subjects, "side commit 1")
	assert.NotContains(subjects, "side commit 2")
}

func TestBranchActivityDetectsForcePush(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@example.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")

	require.NoError(os.WriteFile(filepath.Join(work, "file.txt"), []byte("a\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "commit A")
	commitTestRun(t, work, "git", "push", "origin", "main")

	mgr := New(filepath.Join(dir, "clones"), nil)
	require.NoError(mgr.EnsureClone(t.Context(), "github.com", "acme", "widgets", remote))
	oldTip, err := mgr.ResolveRef(t.Context(), "github.com", "acme", "widgets", "main")
	require.NoError(err)

	commitTestRun(t, work, "git", "checkout", "--orphan", "rewrite")
	commitTestRun(t, work, "git", "rm", "-r", "--cached", ".")
	require.NoError(os.WriteFile(filepath.Join(work, "file.txt"), []byte("b\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "commit B")
	commitTestRun(t, work, "git", "push", "--force", "origin", "HEAD:main")

	require.NoError(mgr.EnsureClone(t.Context(), "github.com", "acme", "widgets", remote))
	newTip, err := mgr.ResolveRef(t.Context(), "github.com", "acme", "widgets", "main")
	require.NoError(err)

	isAncestor, err := mgr.IsAncestor(t.Context(), "github.com", "acme", "widgets", oldTip, newTip)
	require.NoError(err)
	assert.False(isAncestor)
	assert.NotEqual(oldTip, newTip)
}

func TestResolveRefRejectsNonCommitObjects(t *testing.T) {
	require := require.New(t)

	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@example.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")
	require.NoError(os.WriteFile(filepath.Join(work, "file.txt"), []byte("content\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "base")
	commitTestRun(t, work, "git", "push", "origin", "main")

	mgr := New(filepath.Join(dir, "clones"), nil)
	require.NoError(mgr.EnsureClone(t.Context(), "github.com", "acme", "widgets", remote))

	blobSHA := gitSHA(t, work, "HEAD:file.txt")
	_, err := mgr.ResolveRef(t.Context(), "github.com", "acme", "widgets", blobSHA)
	require.Error(err)
	require.ErrorIs(err, ErrNotFound)
}

func TestListBranchCommitsSinceAfterSHATakesPrecedence(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@example.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")

	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "base")
	afterSHA := gitSHA(t, work, "HEAD")

	for _, subject := range []string{"main 1", "main 2"} {
		require.NoError(os.WriteFile(filepath.Join(work, subject+".txt"), []byte(subject+"\n"), 0o644))
		commitTestRun(t, work, "git", "add", ".")
		commitTestRun(t, work, "git", "commit", "-m", subject)
	}
	commitTestRun(t, work, "git", "push", "origin", "main")

	mgr := New(filepath.Join(dir, "clones"), nil)
	require.NoError(mgr.EnsureClone(t.Context(), "github.com", "acme", "widgets", remote))

	commits, err := mgr.ListBranchCommitsSince(
		t.Context(), "github.com", "acme", "widgets", "main", time.Now().Add(24*time.Hour).UTC(), afterSHA,
	)
	require.NoError(err)

	var subjects []string
	for _, commit := range commits {
		subjects = append(subjects, commit.Message)
	}
	assert.Equal([]string{"main 2", "main 1"}, subjects)
}

func TestResolveDefaultBranchFallsBackToOriginHEAD(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@example.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")
	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "base")
	commitTestRun(t, work, "git", "push", "origin", "main")

	mgr := New(filepath.Join(dir, "clones"), nil)
	require.NoError(mgr.EnsureClone(t.Context(), "github.com", "acme", "widgets", remote))

	branch, ref, err := mgr.ResolveDefaultBranch(t.Context(), "github.com", "acme", "widgets", "stale")
	require.NoError(err)
	assert.Equal("main", branch)
	assert.Equal(gitSHA(t, work, "main"), ref)
}
