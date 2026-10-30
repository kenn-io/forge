package github

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
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
	assert.Equal(int64(1), gql.probes.Load(), "four pull requests fit in one probe")
	assert.Equal(int64(4), gql.probedPRs.Load())

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
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	gql := &freshnessGraphQLServer{commentEdit: "2024-06-01T12:00:00Z", mergeState: "CLEAN"}
	server := httptest.NewServer(gql.handler(t))
	t.Cleanup(server.Close)
	client := &mockClient{getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
		return buildOpenPR(number, time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)), nil
	}}
	repo := RepoRef{Owner: "acme", Name: "app", PlatformHost: "github.com"}
	syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Hour, nil, nil)
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil,
			WithGraphQLEndpointForTesting(server.URL)),
	})
	require.NoError(syncer.SyncMROnProvider(ctx, platform.KindGitHub, "github.com", "acme", "app", 1))

	quota := NewQuotaRegistry()
	quota.UpdateSnapshot(HostIdentity("github.com"), QuotaResourceREST, Rate{
		Limit: 5000, Remaining: RateReserveBuffer, Reset: time.Now().UTC().Add(time.Hour),
	})
	syncer.SetQuotaRegistry(quota)

	focus := WatchedMR{Owner: "acme", Name: "app", Number: 1}
	assert.Empty(t, syncer.StaleVisibleMRs(ctx, &focus, nil))
	assert.Zero(t, gql.probes.Load(), "no REST budget for a refresh means no probe either")
}
