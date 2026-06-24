package gitclone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoBrowserListRefsDisambiguatesBranchAndTag(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	mainSHA := gitSHA(t, work, "main")
	commitTestRun(t, work, "git", "checkout", "-b", "release")
	require.NoError(os.WriteFile(filepath.Join(work, "release.txt"), []byte("release\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "release branch")
	branchSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "HEAD:refs/heads/release")
	commitTestRun(t, work, "git", "checkout", "main")
	commitTestRun(t, work, "git", "tag", "release", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/release")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	refs, defaultRef, truncated, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)

	assert.Equal(RepoBrowserRefBranch, defaultRef.Type)
	assert.Equal("main", defaultRef.Name)
	assert.Equal(mainSHA, defaultRef.SHA)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "release", SHA: branchSHA})
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "release", SHA: mainSHA})
}

func TestRepoBrowserListRefsCapsLargeRefSets(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	refs := make([]RepoBrowserRef, RepoBrowserRefLimit+1)
	for i := range refs {
		refs[i] = RepoBrowserRef{Type: RepoBrowserRefBranch, Name: fmt.Sprintf("branch-%04d", i), SHA: fmt.Sprintf("%040d", i)}
	}

	capped, truncated := capRepoBrowserRefs(refs)

	assert.True(truncated)
	require.Len(capped, RepoBrowserRefLimit)
	assert.Equal("branch-0000", capped[0].Name)
	assert.Equal(fmt.Sprintf("branch-%04d", RepoBrowserRefLimit-1), capped[len(capped)-1].Name)
}

func TestEnsureRepoBrowserCloneDoesNotFetchTagsForExistingClone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	mainSHA := gitSHA(t, work, "main")

	commitTestRun(t, work, "git", "tag", "v1.0.0", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/v1.0.0")
	require.NoError(mgr.EnsureRepoBrowserClone(t.Context(), repo))

	refs, _, truncated, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.NotContains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})
}

func TestRefreshRepoBrowserClonesRefreshesRegisteredRepos(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	mainSHA := gitSHA(t, work, "main")

	commitTestRun(t, work, "git", "tag", "v1.0.0", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/v1.0.0")
	mgr.RefreshRepoBrowserClones(t.Context())

	refs, _, truncated, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})
}

func TestRefreshRepoBrowserClonesUsesSeededExistingClones(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	restarted := New(mgr.baseDir, nil)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Updated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "update readme")
	updatedSHA := gitSHA(t, work, "main")
	commitTestRun(t, work, "git", "push", "origin", "main")

	registered, err := restarted.RegisterExistingRepoBrowserClone(t.Context(), repo)
	require.NoError(err)
	require.True(registered)
	restarted.RefreshRepoBrowserClones(t.Context())

	resolved, err := restarted.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{
		Type: RepoBrowserRefBranch,
		Name: "main",
	})
	require.NoError(err)
	assert.Equal(updatedSHA, resolved.SHA)
}

func TestEnsureRepoBrowserCloneDoesNotRefreshExistingClone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	initial := repoBrowserMainRef(t, mgr, repo)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Updated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "update readme")
	updatedSHA := gitSHA(t, work, "main")
	commitTestRun(t, work, "git", "push", "origin", "main")

	require.NoError(mgr.EnsureRepoBrowserClone(t.Context(), repo))
	stale := repoBrowserMainRef(t, mgr, repo)
	assert.Equal(initial.SHA, stale.SHA)

	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	refreshed := repoBrowserMainRef(t, mgr, repo)
	assert.Equal(updatedSHA, refreshed.SHA)
}

func TestRepoBrowserScheduledRefreshContextStaysCancelable(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	_, repo, _ := setupRepoBrowserTestRepo(t)
	mgr := New(filepath.Join(t.TempDir(), "clones-canceled"), nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := mgr.RefreshRepoBrowserClone(ctx, repo)
	require.ErrorIs(err, context.Canceled)
	repos := mgr.repoBrowserReposSnapshot()
	assert.Empty(repos)
}

func TestRepoBrowserRequestRefreshWorkDetachesCallerCancellation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	requestWork := repoBrowserRefreshWorkParent(ctx, repoBrowserRefreshDetachCaller)
	scheduledWork := repoBrowserRefreshWorkParent(ctx, repoBrowserRefreshRespectCaller)

	cancel()

	require.NoError(requestWork.Err())
	assert.ErrorIs(scheduledWork.Err(), context.Canceled)
}

func TestRepoBrowserRefreshFetchesTagsWithoutPruning(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	mainSHA := gitSHA(t, work, "main")

	commitTestRun(t, work, "git", "tag", "v1.0.0", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/v1.0.0")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	refs, _, truncated, err := mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})

	commitTestRun(t, work, "git", "tag", "-d", "v1.0.0")
	commitTestRun(t, work, "git", "push", "origin", ":refs/tags/v1.0.0")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	refs, _, truncated, err = mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})

	commitTestRun(t, work, "git", "tag", "v1.0.1", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "refs/tags/v1.0.1")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	refs, _, truncated, err = mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.1", SHA: mainSHA})

	require.NoError(os.WriteFile(filepath.Join(work, "retag.txt"), []byte("retag\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "retag target")
	movedSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "tag", "-f", "v1.0.1", movedSHA)
	commitTestRun(t, work, "git", "push", "origin", "main")
	commitTestRun(t, work, "git", "push", "--force", "origin", "refs/tags/v1.0.1")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	refs, _, truncated, err = mgr.ListRepoBrowserRefs(t.Context(), repo, "main")
	require.NoError(err)
	assert.False(truncated)
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.0", SHA: mainSHA})
	assert.Contains(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "v1.0.1", SHA: movedSHA})
}

func TestRepoBrowserRefNamesResolveAsExactRefs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	initialSHA := gitSHA(t, work, "main")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("updated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "move main")
	mainSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "tag", "release", mainSHA)
	commitTestRun(t, work, "git", "push", "origin", "main", "refs/tags/release")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	ref, err := mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main"})
	require.NoError(err)
	assert.Equal(mainSHA, ref.SHA)

	_, err = mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main~1"})
	require.ErrorIs(err, ErrNotFound)

	_, err = mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{Type: RepoBrowserRefTag, Name: "release^{}"})
	require.ErrorIs(err, ErrNotFound)

	_, err = mgr.ResolveRepoBrowserRef(t.Context(), repo, RepoBrowserRef{Type: RepoBrowserRefCommit, SHA: initialSHA})
	assert.NoError(err)
}

func TestRepoBrowserListTreeReaderStopsAtEntryLimit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var input strings.Builder
	for i := range RepoBrowserTreeEntryLimit + 1 {
		_, err := fmt.Fprintf(&input, "100644 blob %040d %d\tfile-%05d.txt\x00", i, i, i)
		require.NoError(err)
	}
	canceled := false

	entries, truncated, err := readRepoBrowserTreeEntries(strings.NewReader(input.String()), func() {
		canceled = true
	})

	require.NoError(err)
	assert.True(truncated)
	assert.True(canceled)
	assert.Len(entries, RepoBrowserTreeEntryLimit)
}

func TestRepoBrowserListTreeIncludesTrackedDotfiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	ref := repoBrowserMainRef(t, mgr, repo)

	entries, truncated, err := mgr.ListRepoBrowserTree(t.Context(), repo, ref)
	require.NoError(err)

	var paths []string
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	assert.False(truncated)
	assert.Contains(paths, ".github/workflows/ci.yml")
	assert.Contains(paths, ".gitignore")
	assert.Contains(paths, "README.md")
	assert.Contains(paths, "src/main.go")
	assert.NotContains(paths, ".git")
	assert.Equal(gitSHA(t, work, "main"), ref.SHA)
}

func TestRepoBrowserReadBlobRejectsTraversalAndLargeFiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	largePath := filepath.Join(work, "large.txt")
	require.NoError(os.WriteFile(largePath, []byte(string(make([]byte, RepoBrowserBlobSizeLimit+1))), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "large file")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.ReadRepoBrowserBlob(t.Context(), repo, ref, "../secret.txt")
	require.ErrorIs(err, ErrUnsafePath)

	blob, err := mgr.ReadRepoBrowserBlob(t.Context(), repo, ref, "large.txt")
	require.NoError(err)
	assert.True(blob.TooLarge)
	assert.Equal(int64(RepoBrowserBlobSizeLimit+1), blob.Size)
	assert.Empty(blob.Content)
}

func TestRepoBrowserLastChangedBatchCapsPaths(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, _ := setupRepoBrowserTestRepo(t)
	ref := repoBrowserMainRef(t, mgr, repo)
	paths := make([]string, RepoBrowserLastChangedBatchMax+1)
	for i := range paths {
		paths[i] = "README.md"
	}

	_, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, paths)

	require.Error(err)
	assert.ErrorIs(err, ErrTooManyPaths)
}

func TestRepoBrowserLastChangedFallsBackPastBatchLogLimit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	readmeSHA := gitSHA(t, work, "HEAD")

	for i := range RepoBrowserLastChangedLogLimit + 1 {
		require.NoError(os.WriteFile(filepath.Join(work, "churn.txt"), fmt.Appendf(nil, "%d\n", i), 0o644))
		commitTestRun(t, work, "git", "add", ".")
		commitTestRun(t, work, "git", "commit", "-m", fmt.Sprintf("churn %03d", i))
	}
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	changed, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, []string{"README.md", "churn.txt"})

	require.NoError(err)
	assert.Equal(readmeSHA, changed["README.md"].SHA)
	assert.Equal(gitSHA(t, work, "HEAD"), changed["churn.txt"].SHA)
}

func TestRepoBrowserLastChangedHandlesCommitPrefixedPathsAndUTCTimes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	pathName := "commit:notes.md"
	require.NoError(os.WriteFile(filepath.Join(work, pathName), []byte("notes\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "--date=2026-06-01T12:34:56-07:00", "-m", "commit-prefixed path")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	changed, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, []string{pathName})
	require.NoError(err)

	require.Contains(changed, pathName)
	assert.Equal(gitSHA(t, work, "HEAD"), changed[pathName].SHA)
	assert.Equal(time.Date(2026, 6, 1, 19, 34, 56, 0, time.UTC), changed[pathName].AuthoredAt)
}

func TestRepoBrowserLastChangedTreatsCommitFormatShapedPathAsPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	commitShapedPath := strings.Join([]string{
		strings.Repeat("a", 40),
		"Fake Author",
		"fake@example.com",
		"2026-06-01T12:34:56Z",
		"fake subject",
	}, "\x1f")
	secondPath := "zz-after.md"
	require.NoError(os.WriteFile(filepath.Join(work, commitShapedPath), []byte("literal\n"), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, secondPath), []byte("after\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "commit-shaped path")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)
	wantSHA := gitSHA(t, work, "HEAD")

	changed, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, []string{commitShapedPath, secondPath})
	require.NoError(err)

	require.Contains(changed, commitShapedPath)
	require.Contains(changed, secondPath)
	assert.Equal(wantSHA, changed[commitShapedPath].SHA)
	assert.Equal(wantSHA, changed[secondPath].SHA)
	assert.Equal("commit-shaped path", changed[secondPath].Subject)
}

func TestRepoBrowserFileHistoryIsBoundedAtSelectedSHA(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("two\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "readme two")
	selectedSHA := gitSHA(t, work, "HEAD")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("three\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "readme three")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))

	history, err := mgr.RepoBrowserFileHistory(
		t.Context(),
		repo,
		RepoBrowserRef{Type: RepoBrowserRefCommit, SHA: selectedSHA},
		"README.md",
	)
	require.NoError(err)
	require.NotEmpty(history)
	assert.Equal(selectedSHA, history[0].SHA)
	assert.Equal("readme two", history[0].Subject)
	for _, commit := range history {
		assert.NotEqual("readme three", commit.Subject)
	}
}

func TestRepoBrowserFileHistoryRequiresSelectedTreePath(t *testing.T) {
	require := require.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "later.md"), []byte("later\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "later file")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := RepoBrowserRef{Type: RepoBrowserRefCommit, SHA: gitSHA(t, work, "HEAD~1")}

	_, err := mgr.RepoBrowserFileHistory(t.Context(), repo, ref, "later.md")
	require.ErrorIs(err, ErrNotFound)
}

func TestRepoBrowserCommitDetailRequiresSelectedFileHistory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "other.txt"), []byte("other\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "other file", "-m", "Explain the file change.\n\nKeep the body visible.")
	otherSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "README.md", otherSHA)
	require.ErrorIs(err, ErrCommitOutOfScope)

	commit, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "other.txt", otherSHA)
	require.NoError(err)
	assert.Equal(otherSHA, commit.SHA)
	assert.Equal("other file", commit.Subject)
	assert.Equal("Explain the file change.\n\nKeep the body visible.", commit.Body)
}

func TestRepoBrowserCommitDetailRejectsUnknownFullSHA(t *testing.T) {
	require := require.New(t)
	mgr, repo, _ := setupRepoBrowserTestRepo(t)
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "README.md", strings.Repeat("a", 40))

	require.ErrorIs(err, ErrNotFound)
}

func TestRepoBrowserCommitDetailAcceptsOlderFileHistory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)
	readmeSHA := gitSHA(t, work, "HEAD")

	for i := range RepoBrowserHistoryLimit + 1 {
		require.NoError(os.WriteFile(
			filepath.Join(work, fmt.Sprintf("later-%02d.txt", i)),
			[]byte("later\n"),
			0o644,
		))
		commitTestRun(t, work, "git", "add", ".")
		commitTestRun(t, work, "git", "commit", "-m", fmt.Sprintf("later %02d", i))
	}
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	commit, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "README.md", readmeSHA)
	require.NoError(err)
	assert.Equal(readmeSHA, commit.SHA)
	assert.Equal("initial", commit.Subject)
}

func TestRepoBrowserCommitDetailAcceptsMergeCommitTouchingPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	commitTestRun(t, work, "git", "checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Widgets\n\nFeature\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "feature readme")
	commitTestRun(t, work, "git", "checkout", "main")
	require.NoError(os.WriteFile(filepath.Join(work, "main.txt"), []byte("main\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "main work")
	commitTestRun(t, work, "git", "merge", "--no-ff", "feature", "-m", "merge feature")
	mergeSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	commit, err := mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, "README.md", mergeSHA)
	require.NoError(err)
	assert.Equal(mergeSHA, commit.SHA)
	assert.Equal("merge feature", commit.Subject)
}

func TestRepoBrowserHistoryTreatsPathspecMagicAsLiteral(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	magicPath := ":(glob)*.md"
	require.NoError(os.WriteFile(filepath.Join(work, magicPath), []byte("literal\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "literal pathspec file")
	literalSHA := gitSHA(t, work, "HEAD")

	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("# Widgets\n\nUpdated\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "readme update")
	readmeSHA := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	blob, err := mgr.ReadRepoBrowserBlob(t.Context(), repo, ref, magicPath)
	require.NoError(err)
	assert.Equal("literal\n", blob.Content)

	changed, err := mgr.RepoBrowserLastChanged(t.Context(), repo, ref, []string{magicPath})
	require.NoError(err)
	assert.Equal(literalSHA, changed[magicPath].SHA)

	history, err := mgr.RepoBrowserFileHistory(t.Context(), repo, ref, magicPath)
	require.NoError(err)
	require.NotEmpty(history)
	assert.Equal(literalSHA, history[0].SHA)
	for _, commit := range history {
		assert.NotEqual(readmeSHA, commit.SHA)
	}

	_, err = mgr.RepoBrowserCommitDetail(t.Context(), repo, ref, magicPath, readmeSHA)
	require.ErrorIs(err, ErrCommitOutOfScope)
}

func TestRepoBrowserMarkdownAssetRejectsUnsafeAndOversizedPaths(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr, repo, work := setupRepoBrowserTestRepo(t)

	require.NoError(os.WriteFile(filepath.Join(work, "image.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "page.html"), []byte(`<script>alert(1)</script>`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "script.js"), []byte(`alert(1)`), 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "image.png"), []byte{0x89, 0x50, 0x4e, 0x47}, 0o644))
	require.NoError(os.WriteFile(filepath.Join(work, "huge.png"), []byte(string(make([]byte, RepoBrowserBlobSizeLimit+1))), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "assets")
	commitTestRun(t, work, "git", "push", "origin", "main")
	require.NoError(mgr.RefreshRepoBrowserClone(t.Context(), repo))
	ref := repoBrowserMainRef(t, mgr, repo)

	_, err := mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, "/etc/passwd")
	require.ErrorIs(err, ErrUnsafePath)

	for _, path := range []string{"image.svg", "page.html", "script.js"} {
		_, err = mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, path)
		require.ErrorIs(err, ErrUnsupportedAsset, path)
	}

	asset, err := mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, "image.png")
	require.NoError(err)
	assert.Equal("image/png", asset.MediaType)

	_, err = mgr.ReadRepoBrowserAsset(t.Context(), repo, ref, "huge.png")
	assert.True(errors.Is(err, ErrTooLarge) || errors.Is(err, ErrTooLargeAsset))
}

func setupRepoBrowserTestRepo(t *testing.T) (*Manager, RepoBrowserRepoRef, string) {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	commitTestRun(t, dir, "git", "init", "--bare", "--initial-branch=main", remote)

	work := filepath.Join(dir, "work")
	commitTestRun(t, dir, "git", "clone", remote, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@example.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")
	require.NoError(t, os.MkdirAll(filepath.Join(work, ".github", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(work, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".github", "workflows", "ci.yml"), []byte("name: ci\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".gitignore"), []byte("tmp\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# Widgets\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n"), 0o644))
	commitTestRun(t, work, "git", "add", ".")
	commitTestRun(t, work, "git", "commit", "-m", "initial")
	commitTestRun(t, work, "git", "push", "origin", "main")

	mgr := New(filepath.Join(dir, "clones"), nil)
	repo := RepoBrowserRepoRef{
		Provider:  "github",
		Host:      "github.com",
		Owner:     "acme",
		Name:      "widgets",
		RepoPath:  "acme/widgets",
		RemoteURL: remote,
	}
	require.NoError(t, mgr.EnsureRepoBrowserClone(t.Context(), repo))
	return mgr, repo, work
}

func repoBrowserMainRef(t *testing.T, mgr *Manager, repo RepoBrowserRepoRef) RepoBrowserRef {
	t.Helper()
	_, ref, err := mgr.resolveRepoBrowserDefaultBranch(t.Context(), repo, "main")
	require.NoError(t, err)
	return RepoBrowserRef{Type: RepoBrowserRefBranch, Name: "main", SHA: ref}
}
