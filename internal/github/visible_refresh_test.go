package github

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

func TestStaleVisibleMRsCoversViewedStackAndWorktreePullRequests(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	storedAt := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	gql := &freshnessGraphQLServer{
		commentEdit: "2024-06-01T12:00:00Z", mergeState: "CLEAN", headSHA: "abc123def456",
	}
	server := httptest.NewServer(gql.handler(t))
	t.Cleanup(server.Close)

	client := &mockClient{getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
		return buildOpenPR(number, storedAt), nil
	}}
	syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil,
		[]RepoRef{{Owner: "acme", Name: "app", PlatformHost: "github.com"}},
		time.Hour, nil, nil)
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil,
			WithGraphQLEndpointForTesting(server.URL)),
	})
	now := time.Date(2024, 6, 1, 13, 0, 0, 0, time.UTC)
	syncer.SetClock(func() time.Time { return now })
	ids := make(map[int]int64)
	for number := 1; number <= 5; number++ {
		require.NoError(syncer.SyncMROnProvider(ctx, platform.KindGitHub, "github.com", "acme", "app", number))
		mr, err := d.GetMergeRequest(ctx, "github", "github.com", "acme", "app", number)
		require.NoError(err)
		require.NotNil(mr)
		ids[number] = mr.ID
	}
	// #1 is stacked under #4; #3 is checked out in a worktree.
	mr1, err := d.GetMergeRequest(ctx, "github", "github.com", "acme", "app", 1)
	require.NoError(err)
	require.NotNil(mr1)
	stackID, err := d.UpsertStack(ctx, mr1.RepoID, 1, "feature", false)
	require.NoError(err)
	require.NoError(d.ReplaceStackMembers(ctx, stackID, []db.StackMember{
		{StackID: stackID, MergeRequestID: ids[1], Position: 1},
		{StackID: stackID, MergeRequestID: ids[4], Position: 2},
	}))
	syncer.SetWatchedMRs([]WatchedMR{{Owner: "acme", Name: "app", Number: 3}})

	// GitHub moved #2, #3, #4 and #5. #5 is neither visible, stacked, nor
	// linked, so it is not part of what the user is looking at.
	gql.updatedAt = map[int]string{
		2: "2024-06-01T12:05:00Z", 3: "2024-06-01T12:05:00Z",
		4: "2024-06-01T12:05:00Z", 5: "2024-06-01T12:05:00Z",
	}
	focus := WatchedMR{Owner: "acme", Name: "app", Number: 1}
	visible := []WatchedMR{focus, {Owner: "acme", Name: "app", Number: 2}}

	numbers := func(stale []ProbedMR) []int {
		out := make([]int, 0, len(stale))
		for _, mr := range stale {
			out = append(out, mr.Number)
		}
		slices.Sort(out)
		return out
	}
	stale := syncer.StaleVisibleMRs(ctx, &focus, visible)
	assert.Equal([]int{2, 3, 4}, numbers(stale))
	assert.Equal(int64(1), gql.probes.Load(), "the stack, worktree, and visible rows fit in one probe")
	assert.Equal(int64(3), gql.probedPRs.Load(), "the opened pull request syncs through its own visit")

	// A second click right away probes nothing again.
	assert.Empty(syncer.StaleVisibleMRs(ctx, &focus, visible))
	assert.Equal(int64(1), gql.probes.Load())

	// Once a stale pull request's full read succeeds, its fingerprint
	// vouches for it on the next probe.
	for _, mr := range stale {
		require.NoError(syncer.SyncProbedMR(ctx, platform.KindGitHub, "github.com", mr))
	}
	now = now.Add(visibleProbeInterval)
	assert.Empty(syncer.StaleVisibleMRs(ctx, &focus, visible))
	assert.Equal(int64(2), gql.probes.Load())

	// A read that kept stored CI because the check-run read failed cannot
	// vouch, so the pull request stays stale until a read succeeds.
	gql.set(func(s *freshnessGraphQLServer) { s.updatedAt[2] = "2024-06-01T12:10:00Z" })
	client.checkRunsErr = errors.New("check runs unavailable")
	now = now.Add(visibleProbeInterval)
	stale = syncer.StaleVisibleMRs(ctx, &focus, visible)
	require.Equal([]int{2}, numbers(stale))
	require.NoError(syncer.SyncProbedMR(ctx, platform.KindGitHub, "github.com", stale[0]))
	now = now.Add(visibleProbeInterval)
	assert.Equal([]int{2}, numbers(syncer.StaleVisibleMRs(ctx, &focus, visible)))
}

func TestStaleVisibleMRsLeaveTheForegroundRESTReserveAlone(t *testing.T) {
	h := newVisibleHarness(t)
	// Unknown mergeability makes #2 stale whenever it is probed.
	h.gql.set(func(s *freshnessGraphQLServer) { s.mergeState = "UNKNOWN" })
	require.Len(t, h.stale(t), 1, "with REST quota to spare, the visible row is refreshed")
	probes := h.gql.probes.Load()

	quota := NewQuotaRegistry()
	quota.UpdateSnapshot(HostIdentity("github.com"), QuotaResourceREST, Rate{
		Limit: 5000, Remaining: RateReserveBuffer, Reset: time.Now().UTC().Add(time.Hour),
	})
	h.syncer.SetQuotaRegistry(quota)

	assert.Empty(t, h.stale(t))
	assert.Equal(t, probes, h.gql.probes.Load(), "no REST budget for a refresh means no probe either")
}

// visibleHarness stores acme/app#2 from a full read, then probes it as a
// visible row of #1. Each probe advances the clock past the probe interval.
type visibleHarness struct {
	syncer *Syncer
	client *mockClient
	gql    *freshnessGraphQLServer
	now    time.Time
	// prUpdated is the update time full reads return.
	prUpdated atomic.Value
}

func newVisibleHarness(t *testing.T) *visibleHarness {
	t.Helper()
	h := &visibleHarness{
		gql: &freshnessGraphQLServer{
			commentEdit: "2024-06-01T12:00:00Z", mergeState: "CLEAN", headSHA: "abc123def456",
		},
		now: time.Date(2024, 6, 1, 13, 0, 0, 0, time.UTC),
	}
	server := httptest.NewServer(h.gql.handler(t))
	t.Cleanup(server.Close)
	h.prUpdated.Store(time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC))
	h.client = &mockClient{getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
		pr := buildOpenPR(number, h.prUpdated.Load().(time.Time))
		pr.MergeableState = new("clean")
		return pr, nil
	}}
	h.syncer = NewSyncer(map[string]Client{"github.com": h.client}, openTestDB(t), nil,
		[]RepoRef{{Owner: "acme", Name: "app", PlatformHost: "github.com"}}, time.Hour, nil, nil)
	h.syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil,
			WithGraphQLEndpointForTesting(server.URL)),
	})
	h.syncer.SetClock(func() time.Time { return h.now })
	require.NoError(t, h.syncer.SyncMROnProvider(t.Context(), platform.KindGitHub, "github.com", "acme", "app", 2))
	return h
}

func (h *visibleHarness) stale(t *testing.T) []ProbedMR {
	t.Helper()
	h.now = h.now.Add(visibleProbeInterval)
	focus := WatchedMR{Owner: "acme", Name: "app", Number: 1}
	return h.syncer.StaleVisibleMRs(t.Context(), &focus, []WatchedMR{{Owner: "acme", Name: "app", Number: 2}})
}

func TestVisibleFallbackSeesCIOutcomeAndBaseChanges(t *testing.T) {
	h := newVisibleHarness(t)
	assert.Empty(t, h.stale(t), "an unchanged row with no baseline stays put")

	// Checks finished failing without the pull request's update time moving.
	h.gql.set(func(s *freshnessGraphQLServer) { s.ciState = "FAILURE" })
	assert.Len(t, h.stale(t), 1)

	h.gql.set(func(s *freshnessGraphQLServer) { s.ciState = "" })
	_, err := h.syncer.db.WriteDB().ExecContext(t.Context(),
		"UPDATE forge_merge_requests SET platform_base_sha = 'base-1'")
	require.NoError(t, err)
	h.gql.set(func(s *freshnessGraphQLServer) { s.baseSHA = "base-2" })
	assert.Len(t, h.stale(t), 1, "a base push is stale even when nothing else moved")
}

func TestVisibleStoredUnknownMergeabilityStaysEligible(t *testing.T) {
	h := newVisibleHarness(t)
	assert.Empty(t, h.stale(t), "a resolved, unchanged row stays put")

	// The stored row never learned its mergeability. GitHub now reports a
	// conflict while every other compared field is unchanged.
	_, err := h.syncer.db.WriteDB().ExecContext(t.Context(),
		"UPDATE forge_merge_requests SET mergeable_state = 'unknown'")
	require.NoError(t, err)
	h.gql.set(func(s *freshnessGraphQLServer) { s.mergeState = "DIRTY" })
	assert.Len(t, h.stale(t), 1)
}

func TestVisibleRefreshWaitsOutTheRepositoryCooldown(t *testing.T) {
	h := newVisibleHarness(t)
	h.syncer.featureCooldowns.deferUntil(
		RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "app"},
		platform.RepositoryFeatureMergeRequests, h.now.Add(time.Hour),
	)
	assert.Empty(t, h.stale(t))
	assert.Zero(t, h.gql.probes.Load())
}

func TestIncompleteVisibleReadIsRetriedWithoutABaseline(t *testing.T) {
	require := require.New(t)
	h := newVisibleHarness(t)

	// GitHub moved #2. Its full read brings the stored update time level
	// with the probe, but the check-run read fails, so the stored CI may be
	// stale while every compared field now matches.
	moved := time.Date(2024, 6, 1, 12, 5, 0, 0, time.UTC)
	h.gql.set(func(s *freshnessGraphQLServer) { s.updatedAt = map[int]string{2: moved.Format(time.RFC3339)} })
	h.prUpdated.Store(moved)
	stale := h.stale(t)
	require.Len(stale, 1)
	h.client.checkRunsErr = errors.New("check runs unavailable")
	require.NoError(h.syncer.SyncProbedMR(t.Context(), platform.KindGitHub, "github.com", stale[0]))

	// Only the incomplete-read marker can make the next refresh retry it.
	stale = h.stale(t)
	require.Len(stale, 1)
	h.client.checkRunsErr = nil
	require.NoError(h.syncer.SyncProbedMR(t.Context(), platform.KindGitHub, "github.com", stale[0]))
	assert.Empty(t, h.stale(t), "a complete read vouches again")
}
