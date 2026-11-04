package stacks

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	realdb "go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

const (
	prOpen           = realdb.MergeRequestStateOpen
	prMerged         = realdb.MergeRequestStateMerged
	testRepoCloneURL = "https://github.com/acme/widget.git"
)

func makePR(id int64, number int, head, base string, state realdb.MergeRequestState) realdb.MergeRequest {
	return realdb.MergeRequest{
		ID:               id,
		Number:           number,
		Title:            "PR " + head,
		HeadBranch:       head,
		BaseBranch:       base,
		HeadRepoCloneURL: testRepoCloneURL,
		State:            state,
	}
}

func makePRWithHeadRepo(id int64, number int, head, base, headRepo string, state realdb.MergeRequestState) realdb.MergeRequest {
	pr := makePR(id, number, head, base, state)
	pr.HeadRepoCloneURL = headRepo
	return pr
}

func TestDetectChains_LinearStack(t *testing.T) {
	assert := assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/auth-token", "main", prOpen),
		makePR(2, 101, "feature/auth-retry", "feature/auth-token", prOpen),
		makePR(3, 102, "feature/auth-ui", "feature/auth-retry", prOpen),
	}

	chains := DetectChains(prs, testRepoCloneURL)
	assert.Len(chains, 1)
	assert.Len(chains[0], 3)
	assert.Equal(100, chains[0][0].Number) // base
	assert.Equal(101, chains[0][1].Number)
	assert.Equal(102, chains[0][2].Number) // tip
}

func TestDetectChains_SinglePRNotAStack(t *testing.T) {
	assert := assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/solo", "main", prOpen),
	}
	chains := DetectChains(prs, testRepoCloneURL)
	assert.Empty(chains)
}

func TestDetectChains_ForkPicksLowestNumber(t *testing.T) {
	assert := assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/base", "main", prOpen),
		makePR(2, 102, "feature/child-b", "feature/base", prOpen),
		makePR(3, 101, "feature/child-a", "feature/base", prOpen),
	}

	chains := DetectChains(prs, testRepoCloneURL)
	assert.Len(chains, 1)
	assert.Len(chains[0], 2)
	assert.Equal(100, chains[0][0].Number)
	assert.Equal(101, chains[0][1].Number) // lowest number wins
}

func TestDetectChains_CycleSkipped(t *testing.T) {
	assert := assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "branch-a", "branch-b", prOpen),
		makePR(2, 101, "branch-b", "branch-a", prOpen),
	}
	chains := DetectChains(prs, testRepoCloneURL)
	assert.Empty(chains)
}

func TestDetectChains_PartialMerge(t *testing.T) {
	assert := assert.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/a", "main", prMerged),
		makePR(2, 101, "feature/b", "feature/a", prOpen),
	}
	chains := DetectChains(prs, testRepoCloneURL)
	assert.Len(chains, 1)
	assert.Len(chains[0], 2)
}

func TestDetectChains_ForkDefaultBranchPRDoesNotHideRoot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const fork = "https://github.com/mjacobs/widget.git"
	prs := []realdb.MergeRequest{
		makePRWithHeadRepo(1, 449, "main", "main", fork, prMerged),
		makePR(2, 748, "locate-parser-interface", "main", prOpen),
		makePR(3, 751, "provider-facade-core", "locate-parser-interface", prOpen),
		makePR(4, 752, "provider-jsonl-source-set", "provider-facade-core", prOpen),
	}

	chains := DetectChains(prs, testRepoCloneURL)
	require.Len(chains, 1)
	assert.Equal([]int{748, 751, 752}, stackNumbers(chains[0]))
}

func TestDetectChains_SameRepoSelfEdgeDoesNotHideRoot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	prs := []realdb.MergeRequest{
		makePR(1, 449, "legacy-parser-base", "legacy-parser-base", prMerged),
		makePR(2, 748, "locate-parser-interface", "legacy-parser-base", prOpen),
		makePR(3, 751, "provider-facade-core", "locate-parser-interface", prOpen),
		makePR(4, 752, "provider-jsonl-source-set", "provider-facade-core", prOpen),
	}

	chains := DetectChains(prs, testRepoCloneURL)
	require.Len(chains, 1)
	assert.Equal([]int{748, 751, 752}, stackNumbers(chains[0]))
}

func TestDetectChains_ForkBranchNameDoesNotShadowUpstreamStackBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const fork = "https://github.com/fork/widget.git"
	prs := []realdb.MergeRequest{
		makePRWithHeadRepo(1, 100, "feature/auth", "main", testRepoCloneURL, prOpen),
		makePRWithHeadRepo(2, 101, "feature/auth-ui", "feature/auth", testRepoCloneURL, prOpen),
		makePRWithHeadRepo(3, 90, "feature/auth", "main", fork, prOpen),
	}

	chains := DetectChains(prs, testRepoCloneURL)
	require.Len(chains, 1)
	assert.Equal([]int{100, 101}, stackNumbers(chains[0]))
}

func TestDetectChains_UnknownHeadRepoDoesNotShadowKnownUpstreamStackBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	prs := []realdb.MergeRequest{
		makePRWithHeadRepo(1, 100, "feature/auth", "main", testRepoCloneURL, prOpen),
		makePRWithHeadRepo(2, 101, "feature/auth-ui", "feature/auth", testRepoCloneURL, prOpen),
		makePRWithHeadRepo(3, 90, "feature/auth", "main", "", prOpen),
	}

	chains := DetectChains(prs, testRepoCloneURL)
	require.Len(chains, 1)
	assert.Equal([]int{100, 101}, stackNumbers(chains[0]))
}

func TestDetectChains_UnknownHeadRepoDoesNotChainWithKnownUpstreamStackBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	prs := []realdb.MergeRequest{
		makePRWithHeadRepo(1, 100, "feature/auth", "main", testRepoCloneURL, prOpen),
		makePRWithHeadRepo(2, 101, "feature/auth-ui", "feature/auth", testRepoCloneURL, prOpen),
		makePRWithHeadRepo(3, 90, "feature/fork-ui", "feature/auth", "", prOpen),
	}

	chains := DetectChains(prs, testRepoCloneURL)
	require.Len(chains, 1)
	assert.Equal([]int{100, 101}, stackNumbers(chains[0]))
}

func TestDetectChains_NormalizesRepoCloneURLs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	prs := []realdb.MergeRequest{
		makePRWithHeadRepo(1, 100, "feature/auth", "main", "HTTPS://GITHUB.COM/acme/widget.git/", prOpen),
		makePRWithHeadRepo(2, 101, "feature/auth-ui", "feature/auth", "https://github.com/acme/widget", prOpen),
	}

	chains := DetectChains(prs, "https://github.com/acme/widget.git?ignored=true#fragment")
	require.Len(chains, 1)
	assert.Equal([]int{100, 101}, stackNumbers(chains[0]))
}

func TestDetectChains_DuplicateHeadPrefersOpen(t *testing.T) {
	assert := assert.New(t)
	// Merged PR and open PR share same head branch.
	// Open PR should be preferred for chain building.
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/auth", "main", prMerged),
		makePR(2, 101, "feature/auth-ui", "feature/auth", prOpen),
		makePR(3, 200, "feature/auth", "main", prOpen),
	}

	chains := DetectChains(prs, testRepoCloneURL)
	assert.Len(chains, 1)
	assert.Len(chains[0], 2)
	// Open PR #200 should be base, not merged #100.
	assert.Equal(200, chains[0][0].Number)
	assert.Equal(101, chains[0][1].Number)
}

func TestDetectChains_ForkPrefersOpenOverMerged(t *testing.T) {
	assert := assert.New(t)
	// A -> B (merged, lower number) and A -> C (open, higher number).
	// Should follow A -> C since C is open.
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/base", "main", prOpen),
		makePR(2, 101, "feature/child-merged", "feature/base", prMerged),
		makePR(3, 102, "feature/child-open", "feature/base", prOpen),
	}

	chains := DetectChains(prs, testRepoCloneURL)
	assert.Len(chains, 1)
	assert.Len(chains[0], 2)
	assert.Equal(100, chains[0][0].Number)
	assert.Equal(102, chains[0][1].Number) // open child wins over merged
}

func TestDetectChains_FullyMergedNotAStack(t *testing.T) {
	assert := assert.New(t)
	// All PRs merged — should still detect the chain structure.
	prs := []realdb.MergeRequest{
		makePR(1, 100, "feature/a", "main", prMerged),
		makePR(2, 101, "feature/b", "feature/a", prMerged),
	}
	chains := DetectChains(prs, testRepoCloneURL)
	// Chain exists but all merged — RunDetection filters these out.
	assert.Len(chains, 1)
}

func TestDeriveStackName(t *testing.T) {
	assert := assert.New(t)

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

func stackNumbers(chain []realdb.MergeRequest) []int {
	numbers := make([]int, len(chain))
	for i, pr := range chain {
		numbers[i] = pr.Number
	}
	return numbers
}

func stackNumbersFromMembers(members []realdb.StackMemberWithPR) []int {
	numbers := make([]int, len(members))
	for i, member := range members {
		numbers[i] = member.Number
	}
	return numbers
}

func openTestDB(t *testing.T) *realdb.DB {
	t.Helper()
	return dbtest.Open(t)
}

func TestRunDetection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID, err := reposeed.Seed(ctx, d, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)
	require.NoError(d.UpdateRepoProviderObservation(ctx, repoID, realdb.RepoProviderMetadata{
		CloneURL:      testRepoCloneURL,
		DefaultBranch: "main",
	}, nil, nil))

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
			HeadBranch: pr.head, BaseBranch: pr.base, HeadRepoCloneURL: testRepoCloneURL,
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}

	err = RunDetection(ctx, d, repoID)
	require.NoError(err)

	stack, members, err := d.GetStackForPR(ctx, "github", "github.com", "org", "repo", 101)
	require.NoError(err)
	assert.NotNil(stack)
	assert.Equal("auth", stack.Name)
	assert.Len(members, 3)
	assert.Equal(1, members[0].Position)
	assert.Equal(100, members[0].Number)
}

func TestRunDetectionWithNativeStacksClaimsMembersBeforeInference(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, database, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderObservation(ctx, repoID, realdb.RepoProviderMetadata{
		CloneURL: testRepoCloneURL, DefaultBranch: "main",
	}, nil, nil))
	now := time.Now().UTC()
	prs := []struct {
		number     int
		head, base string
	}{
		{100, "feature/a", "main"},
		{101, "feature/b", "feature/a"},
		{102, "feature/c", "feature/b"},
		{200, "other/a", "main"},
		{201, "other/b", "other/a"},
	}
	for i, pr := range prs {
		_, err := database.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: repoID, PlatformID: int64(i + 1), Number: pr.number,
			Title: "PR " + pr.head, Author: "a", State: prOpen,
			HeadBranch: pr.head, BaseBranch: pr.base,
			HeadRepoCloneURL: testRepoCloneURL,
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}
	require.NoError(database.ReplaceGitHubNativeStack(ctx, realdb.GitHubNativeStack{
		RepoID: repoID, GitHubID: 9001, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native", LastObservedAt: now,
		Members: []realdb.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 102, State: "open", HeadRef: "feature/c", HeadSHA: "ccc"},
			{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "feature/b", HeadSHA: "bbb"},
		},
	}))

	require.NoError(RunDetectionWithNativeStacks(ctx, database, repoID, []int{42}))

	nativeStack, nativeMembers, err := database.GetStackForPRByRepoID(ctx, repoID, 101)
	require.NoError(err)
	require.NotNil(nativeStack)
	assert.Equal(102, nativeStack.BaseNumber)
	assert.True(nativeStack.Native)
	assert.Equal([]int{102, 101}, stackNumbersFromMembers(nativeMembers))
	inferredStack, inferredMembers, err := database.GetStackForPRByRepoID(ctx, repoID, 200)
	require.NoError(err)
	require.NotNil(inferredStack)
	assert.False(inferredStack.Native)
	assert.Equal([]int{200, 201}, stackNumbersFromMembers(inferredMembers))
	stack, _, err := database.GetStackForPRByRepoID(ctx, repoID, 100)
	require.NoError(err)
	assert.Nil(stack)
}

func TestRunDetectionClearsNativeWhenBranchInferenceReclaimsStack(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, database, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderObservation(ctx, repoID, realdb.RepoProviderMetadata{
		CloneURL: testRepoCloneURL, DefaultBranch: "main",
	}, nil, nil))
	now := time.Now().UTC()
	for i, pr := range []struct {
		number     int
		head, base string
	}{
		{100, "feature/a", "main"},
		{101, "feature/b", "feature/a"},
	} {
		_, err := database.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: repoID, PlatformID: int64(i + 1), Number: pr.number,
			Title: "PR " + pr.head, Author: "a", State: prOpen,
			HeadBranch: pr.head, BaseBranch: pr.base,
			HeadRepoCloneURL: testRepoCloneURL,
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}
	require.NoError(database.ReplaceGitHubNativeStack(ctx, realdb.GitHubNativeStack{
		RepoID: repoID, GitHubID: 9001, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native", LastObservedAt: now,
		Members: []realdb.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 100, State: "open", HeadRef: "feature/a", HeadSHA: "aaa"},
			{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "feature/b", HeadSHA: "bbb"},
		},
	}))
	require.NoError(RunDetectionWithNativeStacks(ctx, database, repoID, []int{42}))
	nativeStack, _, err := database.GetStackForPRByRepoID(ctx, repoID, 101)
	require.NoError(err)
	require.NotNil(nativeStack)
	require.True(nativeStack.Native)

	// Without confirmation, branch inference projects the same chain onto
	// the same stack row, which must no longer claim native merge semantics.
	require.NoError(RunDetection(ctx, database, repoID))

	inferredStack, members, err := database.GetStackForPRByRepoID(ctx, repoID, 101)
	require.NoError(err)
	require.NotNil(inferredStack)
	assert.Equal(nativeStack.ID, inferredStack.ID)
	assert.False(inferredStack.Native)
	assert.Equal([]int{100, 101}, stackNumbersFromMembers(members))
}

func TestRunDetectionWithNativeStacksKeepsStackWithClosedMember(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, database, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderObservation(ctx, repoID, realdb.RepoProviderMetadata{
		CloneURL: testRepoCloneURL, DefaultBranch: "main",
	}, nil, nil))
	now := time.Now().UTC()
	prs := []struct {
		number     int
		head, base string
		state      realdb.MergeRequestState
	}{
		{100, "feature/a", "main", prOpen},
		{101, "feature/b", "feature/a", realdb.MergeRequestStateClosed},
		{102, "feature/c", "feature/b", prOpen},
	}
	for i, pr := range prs {
		_, err := database.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: repoID, PlatformID: int64(i + 1), Number: pr.number,
			Title: "PR " + pr.head, Author: "a", State: pr.state,
			HeadBranch: pr.head, BaseBranch: pr.base,
			HeadRepoCloneURL: testRepoCloneURL,
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}
	require.NoError(database.ReplaceGitHubNativeStack(ctx, realdb.GitHubNativeStack{
		RepoID: repoID, GitHubID: 9001, Number: 42, Size: 3,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native", LastObservedAt: now,
		Members: []realdb.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 100, State: "open", HeadRef: "feature/a", HeadSHA: "aaa"},
			{Position: 2, PullRequestNumber: 101, State: "closed", HeadRef: "feature/b", HeadSHA: "bbb"},
			{Position: 3, PullRequestNumber: 102, State: "open", HeadRef: "feature/c", HeadSHA: "ccc"},
		},
	}))

	// The exact-number reader must leave unrelated closed history unread.
	unrelated := makePR(0, 999, "old", "main", realdb.MergeRequestStateClosed)
	unrelated.RepoID, unrelated.PlatformID = repoID, 999
	_, err = database.UpsertMergeRequest(ctx, &unrelated)
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(ctx, `UPDATE forge_merge_requests SET created_at = 'invalid' WHERE repo_id = ? AND number = 999`, repoID)
	require.NoError(err)
	_, err = database.ListMergeRequests(ctx, realdb.ListMergeRequestsOpts{RepoID: repoID, State: "all"})
	require.Error(err)

	require.NoError(RunDetectionWithNativeStacks(ctx, database, repoID, []int{42}))

	stack, members, err := database.GetStackForPRByRepoID(ctx, repoID, 102)
	require.NoError(err)
	require.NotNil(stack)
	assert.Equal(100, stack.BaseNumber)
	assert.Equal([]int{100, 101, 102}, stackNumbersFromMembers(members))
}

func TestRunDetectionWithNativeStacksFallsBackWhenStacksOverlap(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, database, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderObservation(ctx, repoID, realdb.RepoProviderMetadata{
		CloneURL: testRepoCloneURL, DefaultBranch: "main",
	}, nil, nil))
	now := time.Now().UTC()
	prs := []struct {
		number     int
		head, base string
		state      realdb.MergeRequestState
	}{
		{100, "feature/a", "main", prOpen},
		{101, "feature/b", "feature/a", prMerged},
		{102, "feature/c", "feature/b", prOpen},
	}
	for i, pr := range prs {
		_, err := database.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: repoID, PlatformID: int64(i + 1), Number: pr.number,
			Title: "PR " + pr.head, Author: "a", State: pr.state,
			HeadBranch: pr.head, BaseBranch: pr.base,
			HeadRepoCloneURL: testRepoCloneURL,
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}
	// Both stacks claim merged PR 101, which no open-PR observation can
	// arbitrate. Persisting both would evict 101 from whichever stack was
	// written first, and projecting only one of them would strip the other's
	// members of their preceding merge blockers.
	require.NoError(database.ReplaceGitHubNativeStack(ctx, realdb.GitHubNativeStack{
		RepoID: repoID, GitHubID: 9043, Number: 43, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-43", LastObservedAt: now,
		Members: []realdb.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 101, State: "merged", HeadRef: "feature/b", HeadSHA: "bbb"},
			{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "feature/c", HeadSHA: "ccc"},
		},
	}))
	require.NoError(database.ReplaceGitHubNativeStack(ctx, realdb.GitHubNativeStack{
		RepoID: repoID, GitHubID: 9042, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-42", LastObservedAt: now,
		Members: []realdb.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 100, State: "open", HeadRef: "feature/a", HeadSHA: "aaa"},
			{Position: 2, PullRequestNumber: 101, State: "merged", HeadRef: "feature/b", HeadSHA: "bbb"},
		},
	}))

	require.NoError(RunDetectionWithNativeStacks(ctx, database, repoID, []int{42, 43}))

	// Branch inference owns the repository, so PR 102 keeps every preceding
	// member instead of losing the ones the dropped native stack held.
	stack, members, err := database.GetStackForPRByRepoID(ctx, repoID, 102)
	require.NoError(err)
	require.NotNil(stack)
	assert.Equal([]int{100, 101, 102}, stackNumbersFromMembers(members))
}

func TestRunDetectionWithNativeStacksDetectsOverlapAfterUnresolvedMember(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, database, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderObservation(ctx, repoID, realdb.RepoProviderMetadata{
		CloneURL: testRepoCloneURL, DefaultBranch: "main",
	}, nil, nil))
	now := time.Now().UTC()
	prs := []struct {
		number     int
		head, base string
		state      realdb.MergeRequestState
	}{
		{100, "feature/a", "main", prOpen},
		{101, "feature/b", "feature/a", prMerged},
		{102, "feature/c", "feature/b", prOpen},
	}
	for i, pr := range prs {
		_, err := database.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: repoID, PlatformID: int64(i + 1), Number: pr.number,
			Title: "PR " + pr.head, Author: "a", State: pr.state,
			HeadBranch: pr.head, BaseBranch: pr.base,
			HeadRepoCloneURL: testRepoCloneURL,
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}
	require.NoError(database.ReplaceGitHubNativeStack(ctx, realdb.GitHubNativeStack{
		RepoID: repoID, GitHubID: 9043, Number: 43, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-43", LastObservedAt: now,
		Members: []realdb.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 101, State: "merged", HeadRef: "feature/b", HeadSHA: "bbb"},
			{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "feature/c", HeadSHA: "ccc"},
		},
	}))
	// PR 900 has no row yet, and it precedes the member shared with stack 43.
	// Resolution stops at the missing row, so the overlap has to be found from
	// declared membership instead.
	require.NoError(database.ReplaceGitHubNativeStack(ctx, realdb.GitHubNativeStack{
		RepoID: repoID, GitHubID: 9042, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-42", LastObservedAt: now,
		Members: []realdb.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 900, State: "open", HeadRef: "feature/z", HeadSHA: "zzz"},
			{Position: 2, PullRequestNumber: 101, State: "merged", HeadRef: "feature/b", HeadSHA: "bbb"},
		},
	}))

	require.NoError(RunDetectionWithNativeStacks(ctx, database, repoID, []int{42, 43}))

	stack, members, err := database.GetStackForPRByRepoID(ctx, repoID, 102)
	require.NoError(err)
	require.NotNil(stack)
	assert.Equal([]int{100, 101, 102}, stackNumbersFromMembers(members),
		"an overlap hidden behind an unresolved member must still force branch inference")
}

func TestRunDetection_ForkBranchNameDoesNotShadowUpstreamStackBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID, err := reposeed.Seed(ctx, d, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)
	require.NoError(d.UpdateRepoProviderObservation(ctx, repoID, realdb.RepoProviderMetadata{
		CloneURL:      testRepoCloneURL,
		DefaultBranch: "main",
	}, nil, nil))

	now := time.Now()
	prs := []realdb.MergeRequest{
		{
			RepoID: repoID, PlatformID: 1, Number: 100,
			Title: "PR feature/auth", Author: "a", State: prOpen,
			HeadBranch: "feature/auth", BaseBranch: "main",
			HeadRepoCloneURL: testRepoCloneURL,
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		},
		{
			RepoID: repoID, PlatformID: 2, Number: 101,
			Title: "PR feature/auth-ui", Author: "a", State: prOpen,
			HeadBranch: "feature/auth-ui", BaseBranch: "feature/auth",
			HeadRepoCloneURL: testRepoCloneURL,
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		},
		{
			RepoID: repoID, PlatformID: 3, Number: 90,
			Title: "Fork PR feature/auth", Author: "fork", State: prOpen,
			HeadBranch: "feature/auth", BaseBranch: "main",
			HeadRepoCloneURL: "https://github.com/fork/repo.git",
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		},
	}
	for i := range prs {
		_, err := d.UpsertMergeRequest(ctx, &prs[i])
		require.NoError(err)
	}

	require.NoError(RunDetection(ctx, d, repoID))

	stack, members, err := d.GetStackForPR(ctx, "github", "github.com", "org", "repo", 101)
	require.NoError(err)
	require.NotNil(stack)
	assert.Equal("auth", stack.Name)
	assert.Equal([]int{100, 101}, stackNumbersFromMembers(members))
}

func TestRunDetection_FullyMergedStackDeleted(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID, err := reposeed.Seed(ctx, d, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(err)
	require.NoError(d.UpdateRepoProviderObservation(ctx, repoID, realdb.RepoProviderMetadata{
		CloneURL:      testRepoCloneURL,
		DefaultBranch: "main",
	}, nil, nil))

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
			HeadBranch: pr.head, BaseBranch: pr.base, HeadRepoCloneURL: testRepoCloneURL,
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}

	err = RunDetection(ctx, d, repoID)
	require.NoError(err)
	stack, _, err := d.GetStackForPR(ctx, "github", "github.com", "org", "repo", 100)
	require.NoError(err)
	assert.NotNil(stack, "stack should exist while PRs are open")

	// Now mark both PRs as merged and re-detect.
	for _, num := range []int{100, 101} {
		_, err := d.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: repoID, PlatformID: int64(num - 99), Number: num,
			Title: "PR merged", Author: "a", State: "merged",
			HeadRepoCloneURL: testRepoCloneURL,
			HeadBranch:       "feature/" + string(rune('a'+num-100)),
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

	stack2, _, err := d.GetStackForPR(ctx, "github", "github.com", "org", "repo", 100)
	require.NoError(err)
	assert.Nil(stack2, "fully merged stack should be deleted")
}

func TestRunDetectionMatchesFullHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prs     []realdb.MergeRequest
		removed int
		native  []int
		want    [][]int
	}{
		{name: "merged parent", prs: []realdb.MergeRequest{
			makePR(1, 1, "a", "main", prMerged), makePR(2, 2, "b", "a", prOpen),
		}, want: [][]int{{1, 2}}},
		{name: "reused head with another base", prs: []realdb.MergeRequest{
			makePR(1, 1, "a", "older", prMerged), makePR(2, 2, "a", "main", prMerged), makePR(3, 3, "b", "a", prOpen),
		}, want: [][]int{{1, 3}}},
		{name: "fork child", prs: []realdb.MergeRequest{
			makePR(1, 1, "a", "main", prMerged), makePRWithHeadRepo(2, 2, "b", "a", "https://github.com/fork/widget.git", prOpen),
		}, want: [][]int{{1, 2}}},
		{name: "fork main", prs: []realdb.MergeRequest{
			makePRWithHeadRepo(1, 1, "main", "main", "https://github.com/fork/widget.git", prOpen),
			makePR(2, 2, "a", "main", prOpen), makePR(3, 3, "b", "a", prOpen),
		}, want: [][]int{{2, 3}}},
		{name: "removed link", prs: []realdb.MergeRequest{
			makePR(1, 1, "a", "main", prMerged), makePR(2, 2, "b", "a", prMerged), makePR(3, 3, "c", "b", prOpen),
		}, removed: 2},
		{name: "develop to main", prs: []realdb.MergeRequest{
			makePR(1, 1, "develop", "main", prOpen), makePR(2, 2, "a", "develop", prMerged), makePR(3, 3, "b", "a", prOpen),
		}, want: [][]int{{1, 2, 3}}},
		{name: "open duplicate wins", prs: []realdb.MergeRequest{
			makePR(1, 1, "a", "older", prMerged), makePR(2, 2, "a", "main", prOpen),
			makePR(3, 3, "a", "other", prOpen), makePR(4, 4, "b", "a", prOpen),
		}, want: [][]int{{2, 4}}},
		{name: "merged tail", prs: []realdb.MergeRequest{
			makePR(1, 1, "a", "main", prOpen), makePR(2, 2, "b", "a", prMerged), makePR(3, 3, "c", "b", prMerged),
		}, want: [][]int{{1, 2, 3}}},
		{name: "merged ancestor with mixed-case host", prs: []realdb.MergeRequest{
			makePRWithHeadRepo(1, 1, "a", "main", "https://GitHub.com/acme/widget.git", prMerged),
			makePR(2, 2, "b", "a", prOpen),
		}, want: [][]int{{1, 2}}},
		{name: "merged tail with mixed-case scheme and host", prs: []realdb.MergeRequest{
			makePR(1, 1, "a", "main", prOpen),
			makePRWithHeadRepo(2, 2, "b", "a", "HTTPS://GitHub.com/acme/widget.git", prMerged),
		}, want: [][]int{{1, 2}}},
		{name: "lower merged sibling hides open descendant", prs: []realdb.MergeRequest{
			makePR(1, 1, "a", "main", prMerged), makePR(2, 2, "b", "a", prMerged),
			makePR(3, 3, "c", "a", prMerged), makePR(4, 4, "d", "c", prOpen),
		}},
		{name: "child loses reused head group", prs: []realdb.MergeRequest{
			makePR(1, 1, "b", "other", prMerged), makePR(2, 2, "a", "main", prOpen),
			makePR(3, 3, "b", "a", prMerged), makePR(4, 4, "c", "a", prMerged),
		}, want: [][]int{{2, 4}}},
		{name: "clone spellings", prs: []realdb.MergeRequest{
			makePRWithHeadRepo(1, 1, "a", "main", "https://github.com/acme/widget/", prMerged),
			makePRWithHeadRepo(2, 2, "b", "a", "https://github.com/acme/widget.git/", prOpen),
			makePR(3, 3, "c", "b", prMerged),
		}, want: [][]int{{1, 2, 3}}},
		{name: "self edge", prs: []realdb.MergeRequest{
			makePR(1, 1, "main", "main", prMerged), makePR(2, 2, "a", "main", prOpen), makePR(3, 3, "b", "a", prOpen),
		}, want: [][]int{{2, 3}}},
		{name: "native splits component", prs: []realdb.MergeRequest{
			makePR(1, 1, "a", "main", prMerged), makePR(2, 2, "b", "a", prOpen),
			makePR(3, 3, "c", "b", prOpen), makePR(4, 4, "d", "c", prOpen),
		}, native: []int{2, 3}, want: [][]int{{2, 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			ctx := t.Context()
			repoID, err := reposeed.Seed(ctx, d, realdb.GitHubRepoIdentity("", "org", "repo"))
			require.NoError(t, err)
			require.NoError(t, d.UpdateRepoProviderObservation(ctx, repoID, realdb.RepoProviderMetadata{CloneURL: testRepoCloneURL}, nil, nil))
			now := time.Now().UTC()
			for _, pr := range tc.prs {
				pr.RepoID, pr.PlatformID = repoID, pr.ID
				_, err := d.UpsertMergeRequest(ctx, &pr)
				require.NoError(t, err)
			}
			if tc.removed != 0 {
				_, err := d.WriteDB().ExecContext(ctx, `INSERT INTO forge_archive_items (
					repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state
				) VALUES (?, 'merge_request', ?, 'removed', ?, ?, 'removed_upstream')`, repoID, tc.removed, now, now)
				require.NoError(t, err)
			}
			full, err := d.ListMergeRequests(ctx, realdb.ListMergeRequestsOpts{RepoID: repoID, State: "all"})
			require.NoError(t, err)
			full = slices.DeleteFunc(full, func(pr realdb.MergeRequest) bool { return pr.State != prOpen && pr.State != prMerged })
			var expected [][]int
			if len(tc.native) > 0 {
				members := []realdb.GitHubNativeStackMember{
					{Position: 1, PullRequestNumber: 2, State: "open"}, {Position: 2, PullRequestNumber: 3, State: "open"},
				}
				require.NoError(t, d.ReplaceGitHubNativeStack(ctx, realdb.GitHubNativeStack{
					RepoID: repoID, GitHubID: 9001, Number: 42, Size: 2, IsOpen: true,
					BaseRef: "main", GitHubCreatedAt: now, LastObservedAt: now, ContentFingerprint: "native", Members: members,
				}))
				expected = append(expected, tc.native)
				full = slices.DeleteFunc(full, func(pr realdb.MergeRequest) bool { return slices.Contains(tc.native, pr.Number) })
			}
			for _, chain := range DetectChains(full, testRepoCloneURL) {
				if hasOpenMember(chain) {
					expected = append(expected, stackNumbers(chain))
				}
			}
			assert.Equal(t, tc.want, expected)
			require.NoError(t, RunDetectionWithNativeStacks(ctx, d, repoID, []int{42}))
			stacks, members, err := d.ListStacksWithMembers(ctx, "")
			require.NoError(t, err)
			var actual [][]int
			for _, stack := range stacks {
				actual = append(actual, stackNumbersFromMembers(members[stack.ID]))
			}
			assert.ElementsMatch(t, expected, actual)
		})
	}
}

func TestLoadDetectionPRsIgnoresUnlinkedHistory(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, d, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(t, err)
	for _, pr := range []realdb.MergeRequest{
		makePR(1, 1, "feature/a", "main", prOpen),
		makePR(2, 2, "feature/b", "feature/a", prOpen),
		makePR(3, 3, "develop", "main", prOpen),
	} {
		pr.RepoID, pr.PlatformID = repoID, pr.ID
		_, err := d.UpsertMergeRequest(ctx, &pr)
		require.NoError(t, err)
	}
	for number := 10; number < 310; number++ {
		for i, pr := range []realdb.MergeRequest{
			makePRWithHeadRepo(0, number*3, "main", "main", fmt.Sprintf("https://github.com/fork-%d/widget.git", number), prMerged),
			makePR(0, number*3+1, fmt.Sprintf("old/%d", number), "main", prMerged),
			makePR(0, number*3+2, fmt.Sprintf("release/%d", number), "develop", prMerged),
		} {
			pr.RepoID, pr.PlatformID = repoID, int64(number*3+i)
			_, err := d.UpsertMergeRequest(ctx, &pr)
			require.NoError(t, err)
		}
		if number == 12 || number == 309 {
			loaded, err := loadDetectionPRs(ctx, d, repoID, testRepoCloneURL, nil)
			require.NoError(t, err)
			assert.ElementsMatch(t, []int{1, 2, 3, 32}, stackNumbers(loaded))
		}
	}
}

func TestCloneSpellings(t *testing.T) {
	key := normalizeRepoKey(testRepoCloneURL)
	for _, spelling := range cloneSpellings(key) {
		assert.Equal(t, key, normalizeRepoKey(spelling))
	}
}

type countingDetectionPRReader struct {
	*realdb.DB
	headQueries map[string]int
	queries     int
}

func (d *countingDetectionPRReader) ListOpenPRsForStackDetection(ctx context.Context, repoID int64) ([]realdb.MergeRequest, error) {
	d.queries++
	return d.DB.ListOpenPRsForStackDetection(ctx, repoID)
}

func (d *countingDetectionPRReader) ListPRsForStackDetectionByHeads(ctx context.Context, repoID int64, cloneURLs, branches []string) ([]realdb.MergeRequest, error) {
	d.queries++
	for _, branch := range branches {
		d.headQueries[branch]++
	}
	return d.DB.ListPRsForStackDetectionByHeads(ctx, repoID, cloneURLs, branches)
}

func (d *countingDetectionPRReader) ListMergedPRsForStackDetectionByBase(ctx context.Context, repoID int64, base string, afterNumber, limit int) ([]realdb.MergeRequest, error) {
	d.queries++
	return d.DB.ListMergedPRsForStackDetectionByBase(ctx, repoID, base, afterNumber, limit)
}

func TestLoadDetectionPRsFetchesReusedHeadGroupOnce(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, d, realdb.GitHubRepoIdentity("", "org", "repo"))
	require.NoError(t, err)
	prs := []realdb.MergeRequest{
		makePR(1, 1, "staging", "main", prOpen),
		makePR(2, 2, "dev", "other", prMerged),
	}
	for number := 3; number <= 702; number++ {
		prs = append(prs, makePR(int64(number), number, "dev", "staging", prMerged))
	}
	for _, pr := range prs {
		pr.RepoID, pr.PlatformID = repoID, pr.ID
		_, err := d.UpsertMergeRequest(ctx, &pr)
		require.NoError(t, err)
	}
	reader := &countingDetectionPRReader{DB: d, headQueries: make(map[string]int)}
	loaded, err := loadDetectionPRs(ctx, reader, repoID, testRepoCloneURL, nil)
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Len(loaded, 702)
	assert.Empty(DetectChains(loaded, testRepoCloneURL))
	assert.Equal(1, reader.headQueries["dev"])
	assert.LessOrEqual(reader.queries, 706, "700 losing children need only linear probes and one head-group fetch")
}
