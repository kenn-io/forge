package stacks

import (
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	realdb "github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/testutil/dbtest"
)

const (
	prOpen   = realdb.MergeRequestStateOpen
	prMerged = realdb.MergeRequestStateMerged
)

func makePR(id int64, number int, head, base string, state realdb.MergeRequestState) realdb.MergeRequest {
	return realdb.MergeRequest{
		ID:         id,
		Number:     number,
		Title:      "PR " + head,
		HeadBranch: head,
		BaseBranch: base,
		State:      state,
	}
}

func TestDetectChains_LinearStack(t *testing.T) {
	assert := Assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/auth-token", "main", prOpen),
		makePR(2, 101, "feature/auth-retry", "feature/auth-token", prOpen),
		makePR(3, 102, "feature/auth-ui", "feature/auth-retry", prOpen),
	}

	chains := DetectChains(prs)
	assert.Len(chains, 1)
	assert.Len(chains[0], 3)
	assert.Equal(100, chains[0][0].Number) // base
	assert.Equal(101, chains[0][1].Number)
	assert.Equal(102, chains[0][2].Number) // tip
}

func TestDetectChains_SinglePRNotAStack(t *testing.T) {
	assert := Assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/solo", "main", prOpen),
	}
	chains := DetectChains(prs)
	assert.Empty(chains)
}

func TestDetectChains_ForkPicksLowestNumber(t *testing.T) {
	assert := Assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/base", "main", prOpen),
		makePR(2, 102, "feature/child-b", "feature/base", prOpen),
		makePR(3, 101, "feature/child-a", "feature/base", prOpen),
	}

	chains := DetectChains(prs)
	assert.Len(chains, 1)
	assert.Len(chains[0], 2)
	assert.Equal(100, chains[0][0].Number)
	assert.Equal(101, chains[0][1].Number) // lowest number wins
}

func TestDetectChains_CycleSkipped(t *testing.T) {
	assert := Assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "branch-a", "branch-b", prOpen),
		makePR(2, 101, "branch-b", "branch-a", prOpen),
	}
	chains := DetectChains(prs)
	assert.Empty(chains)
}

func TestDetectChains_PartialMerge(t *testing.T) {
	assert := Assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/a", "main", prMerged),
		makePR(2, 101, "feature/b", "feature/a", prOpen),
	}
	chains := DetectChains(prs)
	assert.Len(chains, 1)
	assert.Len(chains[0], 2)
}

func TestDetectChains_DuplicateHeadPrefersOpen(t *testing.T) {
	assert := Assert.New(t)
	// Merged PR and open PR share same head branch.
	// Open PR should be preferred for chain building.
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/auth", "main", prMerged),
		makePR(2, 101, "feature/auth-ui", "feature/auth", prOpen),
		makePR(3, 200, "feature/auth", "main", prOpen),
	}

	chains := DetectChains(prs)
	assert.Len(chains, 1)
	assert.Len(chains[0], 2)
	// Open PR #200 should be base, not merged #100.
	assert.Equal(200, chains[0][0].Number)
	assert.Equal(101, chains[0][1].Number)
}

func TestDetectChains_ForkPrefersOpenOverMerged(t *testing.T) {
	assert := Assert.New(t)
	// A -> B (merged, lower number) and A -> C (open, higher number).
	// Should follow A -> C since C is open.
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/base", "main", prOpen),
		makePR(2, 101, "feature/child-merged", "feature/base", prMerged),
		makePR(3, 102, "feature/child-open", "feature/base", prOpen),
	}

	chains := DetectChains(prs)
	assert.Len(chains, 1)
	assert.Len(chains[0], 2)
	assert.Equal(100, chains[0][0].Number)
	assert.Equal(102, chains[0][1].Number) // open child wins over merged
}

func TestDetectChains_FullyMergedNotAStack(t *testing.T) {
	assert := Assert.New(t)
	// All PRs merged — should still detect the chain structure.
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/a", "main", prMerged),
		makePR(2, 101, "feature/b", "feature/a", prMerged),
	}
	chains := DetectChains(prs)
	// Chain exists but all merged — RunDetection filters these out.
	assert.Len(chains, 1)
}

func TestDeriveStackName(t *testing.T) {
	assert := Assert.New(t)

	// Common prefix on token boundary
	assert.Equal("auth", DeriveStackName([]realdb.MergeRequest{
		makePR(1, 1, "feature/auth-fix", "main", prOpen),
		makePR(2, 2, "feature/auth-retry", "feature/auth-fix", prOpen),
	}))

	// No common prefix -- falls back to base PR title
	assert.Equal("PR branch-x", DeriveStackName([]realdb.MergeRequest{
		makePR(1, 1, "branch-x", "main", prOpen),
		makePR(2, 2, "other-y", "branch-x", prOpen),
	}))

	// Partial word boundary rejected
	assert.Equal("PR feature/authorization", DeriveStackName([]realdb.MergeRequest{
		makePR(1, 1, "feature/authorization", "main", prOpen),
		makePR(2, 2, "feature/authorizer", "feature/authorization", prOpen),
	}))
}

func openTestDB(t *testing.T) *realdb.DB {
	t.Helper()
	return dbtest.Open(t)
}

func TestRunDetection(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID, err := d.UpsertRepo(ctx, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)

	// Create a 3-PR chain.
	now := time.Now()
	for i, pr := range []struct {
		num        int
		head, base string
	}{
		{100, "feature/auth", "main"},
		{101, "feature/auth-retry", "feature/auth"},
		{102, "feature/auth-ui", "feature/auth-retry"},
	} {
		_, err := d.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: repoID, PlatformID: int64(i + 1), Number: pr.num,
			Title: "PR " + pr.head, Author: "a", State: "open",
			HeadBranch: pr.head, BaseBranch: pr.base,
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}

	err = RunDetection(ctx, d, repoID)
	require.NoError(err)

	stack, members, err := d.GetStackForPR(ctx, "org", "repo", 101)
	require.NoError(err)
	assert.NotNil(stack)
	assert.Equal("auth", stack.Name)
	assert.Len(members, 3)
	assert.Equal(1, members[0].Position)
	assert.Equal(100, members[0].Number)
}

func TestRunDetection_FullyMergedStackDeleted(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID, err := d.UpsertRepo(ctx, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)

	now := time.Now()
	// Start with an open chain.
	for i, pr := range []struct {
		num        int
		head, base string
	}{
		{100, "feature/a", "main"},
		{101, "feature/b", "feature/a"},
	} {
		_, err := d.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: repoID, PlatformID: int64(i + 1), Number: pr.num,
			Title: "PR " + pr.head, Author: "a", State: "open",
			HeadBranch: pr.head, BaseBranch: pr.base,
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}

	err = RunDetection(ctx, d, repoID)
	require.NoError(err)
	stack, _, err := d.GetStackForPR(ctx, "org", "repo", 100)
	require.NoError(err)
	assert.NotNil(stack, "stack should exist while PRs are open")

	// Now mark both PRs as merged and re-detect.
	for _, num := range []int{100, 101} {
		_, err := d.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: repoID, PlatformID: int64(num - 99), Number: num,
			Title: "PR merged", Author: "a", State: "merged",
			HeadBranch: "feature/" + string(rune('a'+num-100)),
			BaseBranch: func() string {
				if num == 100 {
					return "main"
				}
				return "feature/a"
			}(),
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}

	err = RunDetection(ctx, d, repoID)
	require.NoError(err)

	stack2, _, err := d.GetStackForPR(ctx, "org", "repo", 100)
	require.NoError(err)
	assert.Nil(stack2, "fully merged stack should be deleted")
}
