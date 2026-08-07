package gitclone

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitsReachableFrom(t *testing.T) {
	ctx := context.Background()
	mgr, shas := setupAncestryClone(t)
	assert := assert.New(t)

	absent := strings.Repeat("d", 40)
	result, err := mgr.CommitsReachableFrom(
		ctx, "github", "example.com", "acme", "widgets", shas["c2"],
		[]string{shas["c1"], shas["c2"], shas["c3"], absent, "not-a-sha"},
	)
	require.NoError(t, err)
	assert.True(result.HeadVerified)
	assert.True(result.Live[shas["c1"]], "ancestor of head is live")
	assert.True(result.Live[shas["c2"]], "head itself is live")
	assert.False(result.Live[shas["c3"]], "side branch is dead")
	assert.False(result.Live[absent], "commit absent from a head-complete clone is dead")
	_, evaluated := result.Live["not-a-sha"]
	assert.False(evaluated, "unrepresentable candidates are omitted, not judged")
}

func TestCommitsReachableFromMissingHead(t *testing.T) {
	ctx := context.Background()
	mgr, shas := setupAncestryClone(t)

	result, err := mgr.CommitsReachableFrom(
		ctx, "github", "example.com", "acme", "widgets", strings.Repeat("e", 40),
		[]string{shas["c1"]},
	)
	require.NoError(t, err)
	assert.False(t, result.HeadVerified)
	assert.Empty(t, result.Live)
}

func TestCommitsReachableFromMissingClone(t *testing.T) {
	ctx := context.Background()
	mgr := New(t.TempDir(), nil)

	result, err := mgr.CommitsReachableFrom(
		ctx, "github", "example.com", "acme", "widgets", strings.Repeat("a", 40),
		[]string{strings.Repeat("b", 40)},
	)
	require.NoError(t, err)
	assert.False(t, result.HeadVerified)
}

func TestCommitsReachableFromVisitBudget(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	mgr, shas := setupAncestryClone(t)
	mgr.ancestryVisitBudget = 1

	// The absent candidate can never be found, so the walk would traverse the
	// whole history; the budget stops it and reports the head unverifiable
	// instead of returning partial verdicts.
	result, err := mgr.CommitsReachableFrom(
		ctx, "github", "example.com", "acme", "widgets", shas["c2"],
		[]string{strings.Repeat("d", 40)},
	)
	require.NoError(err)
	assert.False(result.HeadVerified)
	assert.Empty(result.Live)

	// A budget that covers the history still verifies normally.
	mgr.ancestryVisitBudget = 100
	result, err = mgr.CommitsReachableFrom(
		ctx, "github", "example.com", "acme", "widgets", shas["c2"],
		[]string{shas["c1"], strings.Repeat("d", 40)},
	)
	require.NoError(err)
	assert.True(result.HeadVerified)
	assert.True(result.Live[shas["c1"]])
	assert.False(result.Live[strings.Repeat("d", 40)])
}

func TestCommitsReachableFromRejectsOversizedCommit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	mgr, shas := setupAncestryClone(t)

	// Rebuild the clone with a head whose commit message exceeds the object
	// size cap: the walk must refuse to read it and report the head
	// unverifiable instead of decoding contributor-controlled bulk.
	clonePath, err := mgr.ClonePath("github", "example.com", "acme", "widgets")
	require.NoError(err)
	work := t.TempDir()
	commitTestRun(t, work, "git", "clone", clonePath, work)
	commitTestRun(t, work, "git", "config", "user.email", "alice@test.com")
	commitTestRun(t, work, "git", "config", "user.name", "Alice")
	messagePath := filepath.Join(t.TempDir(), "message.txt")
	require.NoError(os.WriteFile(
		messagePath, bytes.Repeat([]byte("padding padding\n"), (1<<20)/16+64), 0o644,
	))
	require.NoError(os.WriteFile(filepath.Join(work, "huge.txt"), []byte("huge\n"), 0o644))
	commitTestRun(t, work, "git", "add", "huge.txt")
	commitTestRun(t, work, "git", "commit", "-F", messagePath)
	hugeHead := gitSHA(t, work, "HEAD")
	commitTestRun(t, work, "git", "push", "origin", "HEAD:refs/heads/huge")

	result, err := mgr.CommitsReachableFrom(
		ctx, "github", "example.com", "acme", "widgets", hugeHead,
		[]string{shas["c1"]},
	)
	require.NoError(err)
	assert.False(result.HeadVerified, "oversized commit objects must not be decoded")
	assert.Empty(result.Live)
}

func TestCommitsReachableFromNoCandidates(t *testing.T) {
	ctx := context.Background()
	mgr, shas := setupAncestryClone(t)

	result, err := mgr.CommitsReachableFrom(
		ctx, "github", "example.com", "acme", "widgets", shas["c1"], nil,
	)
	require.NoError(t, err)
	assert.True(t, result.HeadVerified)
	assert.Empty(t, result.Live)
}
