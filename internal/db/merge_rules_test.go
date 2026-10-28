package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergeRulesFixture stores a blocked pull request with pending CI and clear
// merge credential rules read against that state.
func mergeRulesFixture(t *testing.T) (*DB, *MergeRequest) {
	t.Helper()
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "rules")
	mr := testMR(repoID, 7)
	mr.PlatformHeadSHA = "head-1"
	mr.MergeableState = "blocked"
	mr.CIStatus = "pending"
	mr.ReviewDecision = "APPROVED"
	_, err := d.UpsertMergeRequest(ctx, mr)
	require.NoError(t, err)
	read, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(t, err)
	applied, err := d.UpdateMergeRequestMergeRules(ctx, read, "clear")
	require.NoError(t, err)
	require.True(t, applied)
	return d, read
}

// resyncMR upserts a provider snapshot of the stored pull request, which
// never carries the merge credential's rules, after change edits it.
func resyncMR(t *testing.T, d *DB, read *MergeRequest, change func(*MergeRequest)) {
	t.Helper()
	snapshot := *read
	snapshot.MergeRules = ""
	snapshot.UpdatedAt = read.UpdatedAt.Add(time.Second)
	change(&snapshot)
	_, err := d.UpsertMergeRequest(t.Context(), &snapshot)
	require.NoError(t, err)
}

func storedMergeRules(t *testing.T, d *DB, read *MergeRequest) string {
	t.Helper()
	mr, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), read.RepoID, read.Number)
	require.NoError(t, err)
	require.NotNil(t, mr)
	return mr.MergeRules
}

func TestMergeRulesSurvivePullRequestSyncsThatKeepTheirState(t *testing.T) {
	t.Parallel()
	d, read := mergeRulesFixture(t)

	resyncMR(t, d, read, func(mr *MergeRequest) { mr.Title = "renamed" })

	assert.Equal(t, "clear", storedMergeRules(t, d, read))
}

func TestMergeRulesBecomeUnknownWhenTheStateTheyWereReadAgainstChanges(t *testing.T) {
	t.Parallel()
	changes := map[string]func(*testing.T, *DB, *MergeRequest){
		"new head": func(t *testing.T, d *DB, read *MergeRequest) {
			resyncMR(t, d, read, func(mr *MergeRequest) { mr.PlatformHeadSHA = "head-2" })
		},
		"retargeted": func(t *testing.T, d *DB, read *MergeRequest) {
			resyncMR(t, d, read, func(mr *MergeRequest) { mr.BaseBranch = "release" })
		},
		"synced merge state": func(t *testing.T, d *DB, read *MergeRequest) {
			resyncMR(t, d, read, func(mr *MergeRequest) { mr.MergeableState = "clean" })
		},
		"CI passed in a pull request sync": func(t *testing.T, d *DB, read *MergeRequest) {
			resyncMR(t, d, read, func(mr *MergeRequest) { mr.CIStatus = "success" })
		},
		"CI passed in a CI refresh": func(t *testing.T, d *DB, read *MergeRequest) {
			require.NoError(t, d.UpdateMRCIStatusForHead(
				t.Context(), read.RepoID, read.Number, "head-1", "success", "[]", false, nil))
		},
		"approval dismissed": func(t *testing.T, d *DB, read *MergeRequest) {
			require.NoError(t, d.UpdateMRReviewActivity(t.Context(), read.ID, "REVIEW_REQUIRED"))
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d, read := mergeRulesFixture(t)

			change(t, d, read)

			assert.Empty(t, storedMergeRules(t, d, read))
		})
	}
}

func TestMergeRulesReadAgainstAnEarlierStateAreNotStored(t *testing.T) {
	t.Parallel()
	d, read := mergeRulesFixture(t)
	resyncMR(t, d, read, func(mr *MergeRequest) { mr.PlatformHeadSHA = "head-2" })

	applied, err := d.UpdateMergeRequestMergeRules(t.Context(), read, "blocked_bypassable")

	require.NoError(t, err)
	assert.False(t, applied)
	assert.Empty(t, storedMergeRules(t, d, read))
}
