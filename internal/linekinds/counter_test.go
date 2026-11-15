package linekinds

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/serverfake"
)

// A pass counts the fixture pull request's diff from its clone, and counts it
// again once the pull request names a newer diff.
func TestPassCountsEachDiffOnce(t *testing.T) {
	serverfake.AcquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	prID := serverfake.SeedPR(t, database, "acme", "widgets", 1)
	fixture, err := testutil.SetupDiffRepo(ctx, t.TempDir(), database)
	require.NoError(err)

	pending, err := database.ListMergeRequestsWithoutLineKinds(ctx, 0, 10)
	require.NoError(err)
	require.Len(pending, 1)
	assert.Equal(prID, pending[0].ID)
	repoID := pending[0].RepoID

	counter := New(database, fixture.Manager)
	require.NoError(counter.Pass(ctx))
	kinds, err := database.ListLineKindsForMRs(ctx, []int64{prID})
	require.NoError(err)
	// handler.go, the new cache.go, and the deleted config.yaml are code;
	// README.md's whitespace change is docs.
	assert.Equal(map[int64]gitclone.LineKinds{prID: {
		Code: gitclone.LineCounts{Additions: 52, Deletions: 11},
		Docs: gitclone.LineCounts{Additions: 1, Deletions: 1},
	}}, kinds)
	pending, err = database.ListMergeRequestsWithoutLineKinds(ctx, 0, 10)
	require.NoError(err)
	assert.Empty(pending)

	// A pushed commit makes the stored counts stale until the next pass.
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, fixture.AltHeadSHA, fixture.BaseSHA, fixture.BaseSHA))
	kinds, err = database.ListLineKindsForMRs(ctx, []int64{prID})
	require.NoError(err)
	assert.Empty(kinds)

	require.NoError(counter.Pass(ctx))
	kinds, err = database.ListLineKindsForMRs(ctx, []int64{prID})
	require.NoError(err)
	// The newer head adds cache_test.go and docs/cache-plan.md.
	assert.Equal(map[int64]gitclone.LineKinds{prID: {
		Code: gitclone.LineCounts{Additions: 67, Deletions: 11},
		Docs: gitclone.LineCounts{Additions: 4, Deletions: 1},
	}}, kinds)
}
