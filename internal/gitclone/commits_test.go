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

func commitTestRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	require.Equal(t, "git", name)
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "command %s %v failed: %s%s", name, args, out, stderr)
}

// setupCommitTestRepo creates a bare repo with 5 commits on a "pr" branch
// forked from "main". Returns (bare repo path, merge base SHA, head SHA).
func setupCommitTestRepo(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", bare)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", bare, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")

	// Initial commit on main = merge base.
	require.NoError(t, os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "base commit")
	commitTestRun(t, work, "git", "push", "origin", "main")
	mergeBase := gitSHA(t, work, "HEAD")

	// PR branch with 5 commits.
	commitTestRun(t, work, "git", "checkout", "-b", "pr")
	for i := 1; i <= 5; i++ {
		fname := filepath.Join(work, "file"+string(rune('0'+i))+".txt")
		require.NoError(t, os.WriteFile(fname, []byte("content\n"), 0o644))
		commitTestRun(t, work, "git", "add", ".")
		commitTestRun(t, work, "git", "commit", "-m", "commit "+string(rune('0'+i)))
	}
	commitTestRun(t, work, "git", "push", "origin", "pr")
	headSHA := gitSHA(t, work, "HEAD")

	return bare, mergeBase, headSHA
}

func gitSHA(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitcmd.New().Output(t.Context(), dir, "rev-parse", ref)
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestListCommits(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	bare, mergeBase, headSHA := setupCommitTestRepo(t)
	mgr := New(filepath.Dir(bare), nil)

	// The bare repo is at dir/remote.git, so host="" owner="" name="remote".
	commits, err := mgr.ListCommits(t.Context(), "", "", "remote", mergeBase, headSHA)
	require.NoError(err)
	assert.Len(commits, 5)

	// Newest first: commit 5, 4, 3, 2, 1.
	assert.Equal("commit 5", commits[0].Message)
	assert.Equal("commit 1", commits[4].Message)

	// All authored by Alice.
	for _, c := range commits {
		assert.Equal("Alice", c.AuthorName)
		assert.False(c.AuthoredAt.IsZero())
		assert.Len(c.SHA, 40)
	}
}

func TestParseCommitLogCapsMetadata(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	line := strings.Join([]string{
		strings.Repeat("a", 40),
		strings.Repeat("n", commitIdentityMaxBytes+20),
		strings.Repeat("e", commitIdentityMaxBytes+20),
		"2024-01-15T12:00:00Z",
		strings.Repeat("c", commitIdentityMaxBytes+20),
		strings.Repeat("m", commitIdentityMaxBytes+20),
		"2024-01-15T12:01:00Z",
		strings.Repeat("s", commitMessageMaxBytes+20),
	}, "\x00")

	commits, err := parseCommitLog([]byte(line + "\n"))
	require.NoError(err)
	require.Len(commits, 1)
	commit := commits[0]
	assert.Len(commit.AuthorName, commitIdentityMaxBytes)
	assert.Len(commit.AuthorEmail, commitIdentityMaxBytes)
	assert.Len(commit.CommitterName, commitIdentityMaxBytes)
	assert.Len(commit.CommitterEmail, commitIdentityMaxBytes)
	assert.Len(commit.Message, commitMessageMaxBytes)
}

func TestListCommits_EmptyRange(t *testing.T) {
	bare, mergeBase, _ := setupCommitTestRepo(t)
	mgr := New(filepath.Dir(bare), nil)

	commits, err := mgr.ListCommits(t.Context(), "", "", "remote", mergeBase, mergeBase)
	require.NoError(t, err)
	assert.Empty(t, commits)
}

func TestParentOfRejectsOptionLikeSHA(t *testing.T) {
	require := require.New(t)

	bare, _, _ := setupCommitTestRepo(t)
	mgr := New(filepath.Dir(bare), nil)
	before, err := os.ReadFile(filepath.Join(bare, "config"))
	require.NoError(err)

	_, err = mgr.ParentOf(t.Context(), "", "", "remote", "--output=config")
	require.Error(err)

	after, err := os.ReadFile(filepath.Join(bare, "config"))
	require.NoError(err)
	require.Equal(before, after)
}

func TestCommitTimelineSinceTag(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")

	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "release commit")
	commitTestRun(t, work, "git", "tag", "v1.0.0")

	for i := 1; i <= 3; i++ {
		fname := filepath.Join(work, "main"+string(rune('0'+i))+".txt")
		require.NoError(os.WriteFile(fname, []byte("content\n"), 0o644))
		commitTestRun(t, work, "git", "add", ".")
		commitTestRun(t, work, "git", "commit", "-m", "main "+string(rune('0'+i)))
	}
	commitTestRun(t, work, "git", "push", "--tags", "origin", "main")

	mgr := New(filepath.Join(dir, "clones"), nil)
	require.NoError(mgr.EnsureClone(t.Context(), "github.com", "acme", "widgets", remote))

	count, points, err := mgr.CommitTimelineSinceTag(
		t.Context(), "github.com", "acme", "widgets", "v1.0.0", 2,
	)
	require.NoError(err)
	assert.Equal(3, count)
	require.Len(points, 2)
	assert.Len(points[0].SHA, 40)
	assert.Equal("main 3", points[0].Message)
	assert.False(points[0].CommittedAt.IsZero())
}

func TestCommitTimelineSinceTagWithoutOriginHEAD(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")

	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "release commit")
	commitTestRun(t, work, "git", "tag", "v1.0.0")
	require.NoError(os.WriteFile(filepath.Join(work, "main1.txt"), []byte("content\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "main 1")
	commitTestRun(t, work, "git", "push", "--tags", "origin", "main")

	mgr := New(filepath.Join(dir, "clones"), nil)
	require.NoError(mgr.EnsureClone(t.Context(), "github.com", "acme", "widgets", remote))
	clonePath, err := mgr.ClonePath("github.com", "acme", "widgets")
	require.NoError(err)
	commitTestRun(t, clonePath, "git", "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")

	count, points, err := mgr.CommitTimelineSinceTag(
		t.Context(), "github.com", "acme", "widgets", "v1.0.0", 2,
	)
	require.NoError(err)
	assert.Equal(1, count)
	require.Len(points, 1)
	assert.Equal("main 1", points[0].Message)
}

func TestListCommits_FirstParent(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", bare)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", bare, work)
	commitTestRun(t, work, "git", "config", "user.email", "test@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Test")

	// Base commit.
	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "base")
	commitTestRun(t, work, "git", "push", "origin", "main")
	mergeBase := gitSHA(t, work, "HEAD")

	// PR branch: one commit, then merge a side branch, then one more commit.
	commitTestRun(t, work, "git", "checkout", "-b", "pr")
	require.NoError(os.WriteFile(filepath.Join(work, "pr1.txt"), []byte("pr1\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "pr commit 1")

	// Side branch with 2 commits.
	commitTestRun(t, work, "git", "checkout", "-b", "side")
	require.NoError(os.WriteFile(filepath.Join(work, "side1.txt"), []byte("s1\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "side commit 1")
	require.NoError(os.WriteFile(filepath.Join(work, "side2.txt"), []byte("s2\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "side commit 2")

	// Merge side into pr.
	commitTestRun(t, work, "git", "checkout", "pr")
	commitTestRun(t, work, "git", "merge", "--no-ff", "side", "-m", "merge side branch")

	// One more commit after merge.
	require.NoError(os.WriteFile(filepath.Join(work, "pr2.txt"), []byte("pr2\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "pr commit 2")
	commitTestRun(t, work, "git", "push", "origin", "pr")
	headSHA := gitSHA(t, work, "HEAD")

	mgr := New(filepath.Dir(bare), nil)
	commits, err := mgr.ListCommits(t.Context(), "", "", "remote", mergeBase, headSHA)
	require.NoError(err)

	// First-parent walk: pr commit 2, merge side branch, pr commit 1.
	// Side commits are NOT included.
	assert.Len(commits, 3)
	assert.Equal("pr commit 2", commits[0].Message)
	assert.Equal("merge side branch", commits[1].Message)
	assert.Equal("pr commit 1", commits[2].Message)
}

func TestParentOf(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	bare, mergeBase, headSHA := setupCommitTestRepo(t)
	mgr := New(filepath.Dir(bare), nil)
	ctx := t.Context()

	commits, err := mgr.ListCommits(ctx, "", "", "remote", mergeBase, headSHA)
	require.NoError(err)
	require.Len(commits, 5)

	parent, err := mgr.ParentOf(ctx, "", "", "remote", commits[0].SHA)
	require.NoError(err)
	assert.Equal(commits[1].SHA, parent)

	parent, err = mgr.ParentOf(ctx, "", "", "remote", commits[4].SHA)
	require.NoError(err)
	assert.Equal(mergeBase, parent)
}

func TestParentOf_RootCommit(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", bare)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", bare, work)
	commitTestRun(t, work, "git", "config", "user.email", "test@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(work, "init.txt"), []byte("init\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "initial")
	commitTestRun(t, work, "git", "push", "origin", "main")
	rootSHA := gitSHA(t, work, "HEAD")

	mgr := New(filepath.Dir(bare), nil)
	parent, err := mgr.ParentOf(t.Context(), "", "", "remote", rootSHA)
	require.NoError(t, err)
	assert.Equal(t, emptyTreeSHA, parent)
}

func TestParentOf_ErrorPropagation(t *testing.T) {
	bare, _, _ := setupCommitTestRepo(t)
	mgr := New(filepath.Dir(bare), nil)

	_, err := mgr.ParentOf(t.Context(), "", "", "remote", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	require.Error(t, err)
}

func TestListCommits_SingleCommit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", bare)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", bare, work)
	commitTestRun(t, work, "git", "config", "user.email", "test@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Test")

	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "base")
	commitTestRun(t, work, "git", "push", "origin", "main")
	mergeBase := gitSHA(t, work, "HEAD")

	commitTestRun(t, work, "git", "checkout", "-b", "pr")
	require.NoError(os.WriteFile(filepath.Join(work, "one.txt"), []byte("one\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "only commit")
	commitTestRun(t, work, "git", "push", "origin", "pr")
	headSHA := gitSHA(t, work, "HEAD")

	mgr := New(filepath.Dir(bare), nil)
	commits, err := mgr.ListCommits(t.Context(), "", "", "remote", mergeBase, headSHA)
	require.NoError(err)
	assert.Len(commits, 1)
	assert.Equal("only commit", commits[0].Message)
}

func TestListCommits_EmptyTreeMergeBase(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// When mergeBase is the empty tree sentinel, ListCommits should return
	// all commits up to headSHA (not use range syntax which would fail).
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", bare)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", bare, work)
	commitTestRun(t, work, "git", "config", "user.email", "test@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Test")

	require.NoError(os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "first")

	require.NoError(os.WriteFile(filepath.Join(work, "b.txt"), []byte("b\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "second")
	commitTestRun(t, work, "git", "push", "origin", "main")
	headSHA := gitSHA(t, work, "HEAD")

	mgr := New(filepath.Dir(bare), nil)
	commits, err := mgr.ListCommits(t.Context(), "", "", "remote", emptyTreeSHA, headSHA)
	require.NoError(err)
	assert.Len(commits, 2)
	assert.Equal("second", commits[0].Message)
	assert.Equal("first", commits[1].Message)
}

func TestParentOf_MergeCommit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	// ParentOf on a merge commit should return the first parent.
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", bare)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", bare, work)
	commitTestRun(t, work, "git", "config", "user.email", "test@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Test")

	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "base")
	firstParentSHA := gitSHA(t, work, "HEAD")

	// Side branch.
	commitTestRun(t, work, "git", "checkout", "-b", "side")
	require.NoError(os.WriteFile(filepath.Join(work, "side.txt"), []byte("side\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "side work")

	// Merge side into main.
	commitTestRun(t, work, "git", "checkout", "main")
	commitTestRun(t, work, "git", "merge", "--no-ff", "side", "-m", "merge side")
	commitTestRun(t, work, "git", "push", "origin", "main")
	mergeSHA := gitSHA(t, work, "HEAD")

	mgr := New(filepath.Dir(bare), nil)
	parent, err := mgr.ParentOf(t.Context(), "", "", "remote", mergeSHA)
	require.NoError(err)
	assert.Equal(firstParentSHA, parent)
}

func TestListCommits_NulInMessage(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", bare)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", bare, work)
	commitTestRun(t, work, "git", "config", "user.email", "test@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Test")

	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "base")
	commitTestRun(t, work, "git", "push", "origin", "main")
	mergeBase := gitSHA(t, work, "HEAD")

	commitTestRun(t, work, "git", "checkout", "-b", "pr")
	require.NoError(os.WriteFile(filepath.Join(work, "nul-message.txt"), []byte("nul\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", `message with \x00 in it`)
	commitTestRun(t, work, "git", "push", "origin", "pr")
	headSHA := gitSHA(t, work, "HEAD")

	mgr := New(filepath.Dir(bare), nil)
	commits, err := mgr.ListCommits(t.Context(), "", "", "remote", mergeBase, headSHA)
	require.NoError(err)
	require.Len(commits, 1)
	assert.Contains(commits[0].Message, `\x00`)
}
