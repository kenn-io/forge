package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

var commentUpdatedAtSelected = regexp.MustCompile(`comments\([^)]*\)\s*\{[^}]*nodes\s*\{[^}]*updatedAt`)

// freshnessGraphQLServer answers freshness probes for every aliased pull
// request with the current comment edit time, so tests change the
// fingerprint by moving it. Other queries get an empty answer.
type freshnessGraphQLServer struct {
	mu          sync.Mutex
	commentEdit string
	mergeState  string
	headSHA     string
	baseSHA     string
	ciState     string
	// updatedAt overrides a pull request's update time by number.
	updatedAt    map[int]string
	probes       atomic.Int64
	probedPRs    atomic.Int64
	probeQueries []string
	// onProbe runs while a probe request is in flight.
	onProbe func()
	// failMergeQueue answers merge queue reads with a GraphQL error.
	failMergeQueue bool
}

func (s *freshnessGraphQLServer) set(update func(*freshnessGraphQLServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(s)
}

func (s *freshnessGraphQLServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		assert.NoError(t, json.UnmarshalRead(r.Body, &req))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "mergeQueue") && s.failMergeQueue {
			_, _ = w.Write([]byte(`{"errors":[{"message":"merge queue unavailable"}]}`))
			return
		}
		if !strings.Contains(req.Query, "fragment Freshness") {
			// Full reads find a pull request with no comments, so they
			// complete instead of keeping stored data.
			pullRequest := `{}`
			if strings.Contains(req.Query, "comments(") {
				pullRequest = `{"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`
			}
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":` + pullRequest + `}}}`))
			return
		}
		s.probes.Add(1)
		if s.onProbe != nil {
			s.onProbe()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.probeQueries = append(s.probeQueries, req.Query)
		data := map[string]any{}
		for name, value := range req.Variables {
			if !strings.HasPrefix(name, "p") {
				continue
			}
			s.probedPRs.Add(1)
			updatedAt := "2024-06-01T12:00:00Z"
			if override, ok := s.updatedAt[int(value.(float64))]; ok {
				updatedAt = override
			}
			pr := map[string]any{
				"updatedAt":        updatedAt,
				"state":            "OPEN",
				"headRefOid":       s.headSHA,
				"mergeStateStatus": s.mergeState,
			}
			// GraphQL answers only requested fields: a comment's edit time
			// appears only if the probe selects it.
			comment := map[string]any{"isMinimized": false}
			if commentUpdatedAtSelected.MatchString(req.Query) {
				comment["updatedAt"] = s.commentEdit
			}
			pr["comments"] = map[string]any{"totalCount": 1, "nodes": []any{comment}}
			// GraphQL answers only requested fields.
			if strings.Contains(req.Query, "baseRefOid") {
				pr["baseRefOid"] = s.baseSHA
			}
			if s.ciState != "" {
				pr["commits"] = map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{
					"statusCheckRollup": map[string]any{"state": s.ciState},
				}}}}
			}
			data["pr"+strings.TrimPrefix(name, "p")] = map[string]any{"pullRequest": pr}
		}
		out, err := json.Marshal(map[string]any{"data": data})
		assert.NoError(t, err)
		_, _ = w.Write(out)
	})
}

func TestWatchedPassSkipsPullRequestsWhoseProbeMatchesTheLastFullRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	gql := &freshnessGraphQLServer{commentEdit: "2024-06-01T12:00:00Z", mergeState: "CLEAN"}
	server := httptest.NewServer(gql.handler(t))
	t.Cleanup(server.Close)

	var fullReads atomic.Int64
	client := &mockClient{getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
		fullReads.Add(1)
		return buildOpenPR(number, time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)), nil
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
	syncer.SetWatchInterval(time.Nanosecond)
	syncer.SetWatchedMRs([]WatchedMR{{Owner: "acme", Name: "app", Number: 7}})

	stored := func() *db.MergeRequest {
		mr, err := d.GetMergeRequest(ctx, "github", "github.com", "acme", "app", 7)
		require.NoError(err)
		require.NotNil(mr)
		return mr
	}

	// No baseline yet: the probe runs, then the full read.
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(1), fullReads.Load())
	assert.Equal(int64(1), gql.probes.Load())
	stale := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := d.WriteDB().ExecContext(ctx,
		"UPDATE forge_merge_requests SET detail_fetched_at = ? WHERE id = ?", stale, stored().ID)
	require.NoError(err)

	// Unchanged: the probe vouches for the stored detail, so no full read,
	// and the detail still counts as freshly read.
	now = now.Add(time.Minute)
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(1), fullReads.Load())
	assert.Equal(int64(2), gql.probes.Load())
	require.NotNil(stored().DetailFetchedAt)
	assert.True(stored().DetailFetchedAt.After(stale), "a matching probe must refresh the detail marker")

	// A comment edit changes the fingerprint and forces a full read.
	gql.mu.Lock()
	gql.commentEdit = "2024-06-01T12:30:00Z"
	gql.mu.Unlock()
	now = now.Add(time.Minute)
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(2), fullReads.Load())

	// Mergeability GitHub is still computing never vouches, even when the
	// probe answers the same on consecutive passes.
	gql.mu.Lock()
	gql.mergeState = "UNKNOWN"
	gql.mu.Unlock()
	now = now.Add(time.Minute)
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(3), fullReads.Load())
	now = now.Add(time.Minute)
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(4), fullReads.Load(), "an unchanged UNKNOWN probe still reads in full")

	// A matching probe cannot stand in for a full read forever.
	gql.mu.Lock()
	gql.mergeState = "CLEAN"
	gql.mu.Unlock()
	now = now.Add(time.Minute)
	syncer.syncWatchedMRs(ctx) // records a new baseline
	assert.Equal(int64(5), fullReads.Load())
	now = now.Add(prFreshnessFullReadInterval)
	syncer.syncWatchedMRs(ctx)
	assert.Equal(int64(6), fullReads.Load())
}

func TestFreshnessProbeBatchesTwentyFivePullRequestsPerRequest(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	gql := &freshnessGraphQLServer{commentEdit: "2024-06-01T12:00:00Z", mergeState: "CLEAN"}
	server := httptest.NewServer(gql.handler(t))
	t.Cleanup(server.Close)
	fetcher := NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil,
		WithGraphQLEndpointForTesting(server.URL))

	refs := make([]PullRequestFreshnessRef, 0, 60)
	for i := range 60 {
		refs = append(refs, PullRequestFreshnessRef{Owner: "acme", Name: "app", Number: i + 1})
	}
	fingerprints, err := fetcher.FetchPullRequestFreshness(t.Context(), refs)
	require.NoError(err)
	assert.Len(fingerprints, 60)
	assert.Equal(int64(3), gql.probes.Load(), "60 pull requests fit in 3 requests of 25")
	assert.Equal(int64(60), gql.probedPRs.Load())
	for _, query := range gql.probeQueries {
		assert.NotContains(query, "acme", "owner and name travel as variables")
	}
}

func TestFreshnessProbeIsolatesAMissingPullRequest(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":{
			"pr0":{"pullRequest":{"state":"OPEN","mergeStateStatus":"BLOCKED"}},
			"pr1":{"pullRequest":null}},
			"errors":[{"type":"NOT_FOUND","path":["pr1","pullRequest"],"message":"Could not resolve"}]}`)
	}))
	t.Cleanup(server.Close)
	fetcher := NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil,
		WithGraphQLEndpointForTesting(server.URL))

	found := PullRequestFreshnessRef{Owner: "acme", Name: "app", Number: 1}
	missing := PullRequestFreshnessRef{Owner: "acme", Name: "app", Number: 2}
	fingerprints, err := fetcher.FetchPullRequestFreshness(
		t.Context(), []PullRequestFreshnessRef{found, missing},
	)
	require.NoError(err)
	assert.Contains(fingerprints, found)
	assert.NotContains(fingerprints, missing)
}

// watchedFreshnessHarness runs the watched pass for acme/app#7 against a
// fake probe server and a mock REST client that counts full reads.
type watchedFreshnessHarness struct {
	syncer    *Syncer
	client    *mockClient
	gql       *freshnessGraphQLServer
	fullReads atomic.Int64
	now       time.Time
}

func newWatchedFreshnessHarness(t *testing.T) *watchedFreshnessHarness {
	t.Helper()
	h := &watchedFreshnessHarness{
		gql: &freshnessGraphQLServer{
			commentEdit: "2024-06-01T12:00:00Z", mergeState: "CLEAN", baseSHA: "base-1",
		},
		now: time.Date(2024, 6, 1, 13, 0, 0, 0, time.UTC),
	}
	server := httptest.NewServer(h.gql.handler(t))
	t.Cleanup(server.Close)
	h.client = &mockClient{getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
		h.fullReads.Add(1)
		return buildOpenPR(number, time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)), nil
	}}
	h.syncer = NewSyncer(map[string]Client{"github.com": h.client}, openTestDB(t), nil,
		[]RepoRef{{Owner: "acme", Name: "app", PlatformHost: "github.com"}},
		time.Hour, nil, nil)
	h.syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil,
			WithGraphQLEndpointForTesting(server.URL)),
	})
	h.syncer.SetClock(func() time.Time { return h.now })
	h.syncer.SetWatchInterval(time.Nanosecond)
	h.syncer.SetWatchedMRs([]WatchedMR{{Owner: "acme", Name: "app", Number: 7}})
	return h
}

func (h *watchedFreshnessHarness) pass(t *testing.T) {
	t.Helper()
	h.now = h.now.Add(time.Minute)
	h.syncer.syncWatchedMRs(t.Context())
}

func TestSyncWatchedMRsPacesProbes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		owners   int
		prs      int
		limit    int
		readCost int
		interval time.Duration
	}{
		{name: "fanout", owners: 16, prs: 57, limit: 500, readCost: 20, interval: 5 * time.Minute},
		{name: "100-per-hour", owners: 3, prs: 3, limit: 100, readCost: 20, interval: 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			d := openTestDB(t)
			budget := NewSyncBudgetWithEssentialReserve(tc.limit)
			now := budget.windowStart
			budget.now = func() time.Time { return now }
			gql := &freshnessGraphQLServer{mergeState: "CLEAN"}
			var reads []int
			client := &mockClient{getPullRequestFn: func(ctx context.Context, owner, name string, number int) (*gh.PullRequest, error) {
				// Charge the detail read through the real budget accounting.
				_, admitted, _ := budget.trySpendForTransport(tc.readCost, syncSpendKind(ctx), false)
				require.True(t, admitted, "watched admission must leave capacity for the detail read")
				reads = append(reads, number)
				return buildOpenPR(number, now), nil
			}}
			var repos []RepoRef
			var watched []WatchedMR
			for i := range tc.owners {
				owner := fmt.Sprintf("owner-%d", i)
				repos = append(repos, RepoRef{Owner: owner, Name: "app", PlatformHost: "github.com"})
				_, err := reposeed.Seed(t.Context(), d, verifiedGitHubRepoIdentity("github.com", owner, "app"))
				require.NoError(t, err)
			}
			for i := range tc.prs {
				number := i + 1
				watched = append(watched, WatchedMR{Owner: repos[i%tc.owners].Owner, Name: "app", Number: number})
			}
			syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil,
				repos, tc.interval, nil, map[string]*SyncBudget{"github.com": budget})
			t.Cleanup(syncer.Stop)
			syncer.SetClock(func() time.Time { return now })
			syncer.SetWatchInterval(30 * time.Second)
			syncer.SetWatchedMRs(watched)
			server := httptest.NewServer(gql.handler(t))
			t.Cleanup(server.Close)
			syncer.SetFetchers(map[string]*GraphQLFetcher{
				"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, budget,
					WithGraphQLEndpointForTesting(server.URL)),
			})
			start := now
			probeOnlyPasses := 0
			for elapsed := time.Duration(0); elapsed < time.Hour; elapsed += 30 * time.Second {
				now = start.Add(elapsed)
				// Keep probes changed so every admitted pass needs a full read.
				gql.set(func(s *freshnessGraphQLServer) { s.commentEdit = now.Format(time.RFC3339Nano) })
				probesBefore, readsBefore := gql.probes.Load(), len(reads)
				syncer.syncWatchedMRs(t.Context())
				if elapsed == 0 {
					require.NotEmpty(t, reads, "an idle budget must admit the first watched read in the first pass")
				}
				if gql.probes.Load() > probesBefore && len(reads) == readsBefore {
					probeOnlyPasses++
				}
			}
			assert.NotEmpty(reads, "watched details must make progress over an hour")
			assert.Zero(probeOnlyPasses, "each probing pass must leave capacity for a full read")
		})
	}
}

func TestFailedCIReadCannotVouchForTheProbe(t *testing.T) {
	assert := assert.New(t)
	h := newWatchedFreshnessHarness(t)

	// The full read keeps stored CI because the check-run read failed, so
	// its probe cannot stand in for the next read.
	h.client.checkRunsErr = errors.New("check runs unavailable")
	h.pass(t)
	h.pass(t)
	assert.Equal(int64(2), h.fullReads.Load())

	// Once CI reads succeed, the probe vouches again.
	h.client.checkRunsErr = nil
	h.pass(t)
	h.pass(t)
	assert.Equal(int64(3), h.fullReads.Load())
}

func TestBaseOnlyPushForcesAFullRead(t *testing.T) {
	assert := assert.New(t)
	h := newWatchedFreshnessHarness(t)

	h.pass(t)
	h.pass(t)
	assert.Equal(int64(1), h.fullReads.Load())

	h.gql.set(func(s *freshnessGraphQLServer) { s.baseSHA = "base-2" })
	h.pass(t)
	assert.Equal(int64(2), h.fullReads.Load())
}

func TestMatchingProbeStillRefreshesPendingCI(t *testing.T) {
	assert := assert.New(t)
	h := newWatchedFreshnessHarness(t)
	status, conclusion := "in_progress", ""
	h.client.checkRuns = []*gh.CheckRun{{Name: new("build"), Status: &status, Conclusion: &conclusion}}

	h.pass(t)
	h.pass(t)
	reads := h.client.getCombinedCalls.Load()
	h.pass(t)
	assert.Equal(int64(1), h.fullReads.Load(), "a matching probe skips the full read")
	assert.Greater(h.client.getCombinedCalls.Load(), reads, "pending CI is still refreshed")
}

func TestRepositoryInPullRequestCooldownIsNotProbed(t *testing.T) {
	h := newWatchedFreshnessHarness(t)
	h.syncer.featureCooldowns.deferUntil(
		RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "app"},
		platform.RepositoryFeatureMergeRequests, h.now.Add(time.Hour),
	)
	h.pass(t)
	assert.Zero(t, h.gql.probes.Load())
}

func TestMatchingProbeRevalidatesCompletedCI(t *testing.T) {
	assert := assert.New(t)
	h := newWatchedFreshnessHarness(t)
	status, conclusion := "completed", "success"
	h.client.checkRuns = []*gh.CheckRun{{Name: new("build"), Status: &status, Conclusion: &conclusion}}

	h.pass(t)
	h.pass(t)
	reads := h.client.getCombinedCalls.Load()
	h.pass(t)
	assert.Equal(int64(1), h.fullReads.Load(), "a matching probe skips the full read")
	assert.Greater(h.client.getCombinedCalls.Load(), reads,
		"completed checks can swap results without changing counts, so CI is still revalidated")
}

func TestWatchedPassNotifiesWhenEveryProbeMatches(t *testing.T) {
	h := newWatchedFreshnessHarness(t)
	var completed atomic.Int64
	h.syncer.SetOnWatchedMRSyncCompleted(func() { completed.Add(1) })

	h.pass(t)
	h.pass(t)
	assert.Equal(t, int64(1), h.fullReads.Load())
	assert.Equal(t, int64(2), completed.Load(), "a pass that only refreshed from probes still notifies")
}

func TestMergeQueueFailureDoesNotBlockTheBaseline(t *testing.T) {
	h := newWatchedFreshnessHarness(t)
	h.gql.set(func(s *freshnessGraphQLServer) { s.failMergeQueue = true })

	h.pass(t)
	h.pass(t)
	assert.Equal(t, int64(1), h.fullReads.Load(),
		"merge queue state is not part of the fingerprint, so its failure cannot force full reads")
}

func TestSupersededReadCannotVouchForTheProbe(t *testing.T) {
	assert := assert.New(t)
	h := newWatchedFreshnessHarness(t)
	var supersede atomic.Bool
	supersede.Store(true)
	h.client.listCheckRunsForRefFn = func(ctx context.Context, _, _, _ string) ([]*gh.CheckRun, error) {
		if supersede.Load() {
			// A concurrent sync advances the snapshot mid-read, so this read's
			// datasets are not applied.
			_, err := h.syncer.db.WriteDB().ExecContext(ctx,
				"UPDATE forge_merge_requests SET snapshot_revision = snapshot_revision + 1")
			require.NoError(t, err)
		}
		return nil, nil
	}

	h.pass(t)
	supersede.Store(false)
	h.pass(t)
	assert.Equal(int64(2), h.fullReads.Load(), "a superseded read leaves the next pass reading in full")
	h.pass(t)
	assert.Equal(int64(2), h.fullReads.Load(), "a read that applied vouches again")
}

func TestFreshnessProbeIsActiveDetailWork(t *testing.T) {
	h := newWatchedFreshnessHarness(t)
	repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "app"}
	bucket, err := h.syncer.bucketKeyForRepo(repo, false)
	require.NoError(t, err)
	var archiveYields atomic.Bool
	h.gql.set(func(s *freshnessGraphQLServer) {
		s.onProbe = func() {
			archiveYields.Store(h.syncer.higherPriorityProviderWorkActive(bucket, archive.PriorityFullArchive))
		}
	})

	h.pass(t)
	assert.True(t, archiveYields.Load(), "archive work must see the probe while it runs")
}
