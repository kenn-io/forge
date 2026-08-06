package gitclone

import (
	"context"
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
	require.NoError(t, err)
	assert.False(t, result.HeadVerified)
	assert.Empty(t, result.Live)

	// A budget that covers the history still verifies normally.
	mgr.ancestryVisitBudget = 100
	result, err = mgr.CommitsReachableFrom(
		ctx, "github", "example.com", "acme", "widgets", shas["c2"],
		[]string{shas["c1"], strings.Repeat("d", 40)},
	)
	require.NoError(t, err)
	assert.True(t, result.HeadVerified)
	assert.True(t, result.Live[shas["c1"]])
	assert.False(t, result.Live[strings.Repeat("d", 40)])
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
