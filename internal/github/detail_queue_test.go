package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestDetailQueueIgnoresUntrackedOpenItemVolume(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := dbtest.Open(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	tracked := RepoRef{Owner: "acme", Name: "selected", PlatformHost: "github.com"}
	trackedID, err := reposeed.Seed(ctx, d, verifiedDBRepoIdentity(platformRepoRef(tracked)))
	require.NoError(err)
	untrackedID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", "acme", "legacy"))
	require.NoError(err)

	for number := 1; number <= 2; number++ {
		_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID: trackedID, PlatformID: int64(number), Number: number,
			State: "open", Title: "Tracked PR", HeadBranch: "feature", BaseBranch: "main",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
		_, err = d.UpsertIssue(ctx, &db.Issue{
			RepoID: trackedID, PlatformID: int64(number + 10), Number: number,
			State: "open", Title: "Tracked issue",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}
	syncer := NewSyncer(nil, d, nil, []RepoRef{tracked}, time.Minute, nil, nil)
	baselineItems := syncer.buildDetailQueueItems(ctx, syncer.TrackedRepos())
	require.Len(baselineItems, 4)
	baselineAllocs := testing.AllocsPerRun(10, func() {
		syncer.buildDetailQueueItems(ctx, syncer.TrackedRepos())
	})

	for number := 1; number <= 2000; number++ {
		_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID: untrackedID, PlatformID: int64(number + 100), Number: number,
			State: "open", Title: "Untracked PR", HeadBranch: "feature", BaseBranch: "main",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
		_, err = d.UpsertIssue(ctx, &db.Issue{
			RepoID: untrackedID, PlatformID: int64(number + 10000), Number: number,
			State: "open", Title: "Untracked issue",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}

	assert.ElementsMatch(baselineItems, syncer.buildDetailQueueItems(ctx, syncer.TrackedRepos()))
	assert.Equal(4, syncer.countOverdueDetails(ctx))
	withUntrackedAllocs := testing.AllocsPerRun(10, func() {
		syncer.buildDetailQueueItems(ctx, syncer.TrackedRepos())
	})
	assert.Less(withUntrackedAllocs, 2*baselineAllocs,
		"queue allocations grew from %.0f to %.0f after adding untracked open items",
		baselineAllocs, withUntrackedAllocs)
}

// TestDetailQueueConflictedPullRequestIgnoresUnreportedRequiredChecks pins
// why a pull request with merge conflicts is re-read. GitHub runs no pull
// request workflows on it, so a required check that never reported stays
// expected until the author pushes; it must not make the pull request due on
// every pass. A check that is still running can finish without the pull
// request changing, so it keeps the pull request due.
func TestDetailQueueConflictedPullRequestIgnoresUnreportedRequiredChecks(t *testing.T) {
	t.Parallel()
	const (
		unreported = `[{"name":"CI gate","status":"expected","required":true,"origin":"required_rule"},` +
			`{"name":"lint","status":"completed","conclusion":"success"}]`
		running = `[{"name":"CI gate","status":"expected","required":true,"origin":"required_rule"},` +
			`{"name":"lint","status":"in_progress"}]`
	)
	cases := []struct {
		name           string
		mergeableState string
		checks         string
		wantPending    bool
	}{
		{"conflicted with only unreported required checks", "dirty", unreported, false},
		{"conflicted with a running check", "dirty", running, true},
		{"mergeable with unreported required checks", "clean", unreported, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			ctx := t.Context()
			d := dbtest.Open(t)
			now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			repoID, err := reposeed.Seed(ctx, d, verifiedDBRepoIdentity(platformRepoRef(repo)))
			require.NoError(err)
			fetched := now
			_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
				RepoID: repoID, PlatformID: 1, Number: 1,
				State: "open", Title: "Feature", HeadBranch: "feature", BaseBranch: "main",
				CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
				MergeableState: tc.mergeableState, CIStatus: "pending",
				CIChecksJSON: tc.checks, CIHadPending: true, DetailFetchedAt: &fetched,
			})
			require.NoError(err)
			syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, nil)

			items := syncer.buildDetailQueueItems(ctx, syncer.TrackedRepos())

			require.Len(items, 1)
			require.Equal(tc.wantPending, items[0].CIHadPending)
		})
	}
}
