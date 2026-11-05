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
	platformgithub "go.kenn.io/forge/platform/github"
)

var (
	commentUpdatedAtSelected = regexp.MustCompile(`comments\([^)]*\)\s*\{[^}]*nodes\s*\{[^}]*updatedAt`)
	threadCommentSelection   = regexp.MustCompile(`(?s)reviewThreads\([^)]*\)\s*\{.*?comments\([^)]*\)\s*\{.*?nodes\s*\{([^}]*)\}`)
)

// freshnessGraphQLServer answers freshness probes for every aliased pull
// request with the current comment edit time, so tests change the
// fingerprint by moving it. Other queries get an empty answer.
type freshnessGraphQLServer struct {
	mu                  sync.Mutex
	commentEdit         string
	mergeState          string
	headSHA             string
	baseSHA             string
	ciState             string
	threadEdit          string
	resolved            bool
	reviewMinimized     bool
	issueMinimized      bool
	reviewDecision      string
	commentCount        int
	threadCount         int
	threadCommentCount  int
	failIssueVisibility bool
	visibilityReads     atomic.Int64
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
		if !strings.Contains(req.Query, "fragment Freshness") && !strings.Contains(req.Query, "fragment IssueFreshness") {
			// Full reads find a pull request with no comments, so they
			// complete instead of keeping stored data.
			kind, item := "pullRequest", `{}`
			if strings.Contains(req.Query, "issue(number:") {
				kind = "issue"
				if s.failIssueVisibility {
					_, _ = w.Write([]byte(`{"errors":[{"message":"issue visibility unavailable"}]}`))
					return
				}
			}
			if strings.Contains(req.Query, "comments(") {
				s.visibilityReads.Add(1)
				item = `{"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`
				if kind == "pullRequest" && strings.Contains(req.Query, "reviewDecision") {
					s.mu.Lock()
					item = fmt.Sprintf(`{"reviewDecision":%q,"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`, s.reviewDecision)
					s.mu.Unlock()
				}
			}

			_, _ = fmt.Fprintf(w, `{"data":{"repository":{"%s":%s}}}`, kind, item)
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
			pr["comments"] = map[string]any{"totalCount": max(1, s.commentCount), "nodes": []any{comment}}
			if strings.Contains(req.Query, "reviewDecision") {
				pr["reviewDecision"] = s.reviewDecision
			}
			thread := map[string]any{"isResolved": s.resolved, "isOutdated": false}
			if selection := threadCommentSelection.FindStringSubmatch(req.Query); len(selection) > 0 {
				thread["path"] = "main.go"
				thread["line"] = 7
				comment := map[string]any{}
				if strings.Contains(selection[1], "updatedAt") {
					comment["updatedAt"] = s.threadEdit
				}
				if strings.Contains(selection[1], "isMinimized") {
					comment["isMinimized"] = s.reviewMinimized
				}
				if strings.Contains(selection[1], "minimizedReason") {
					comment["minimizedReason"] = "off-topic"
				}
				thread["comments"] = map[string]any{"totalCount": max(1, s.threadCommentCount), "nodes": []any{
					comment,
				}}
			}
			pr["reviewThreads"] = map[string]any{"totalCount": max(1, s.threadCount), "nodes": []any{thread}}
			// GraphQL answers only requested fields.
			if strings.Contains(req.Query, "baseRefOid") {
				pr["baseRefOid"] = s.baseSHA
			}
			if s.ciState != "" {
				pr["commits"] = map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{
					"statusCheckRollup": map[string]any{"state": s.ciState},
				}}}}
			}
			alias := "pr" + strings.TrimPrefix(name, "p")
			kind := "pullRequest"
			if regexp.MustCompile(alias + `: repository[^}]*\{ issue\(`).MatchString(req.Query) {
				kind = "issue"
				pr = map[string]any{"updatedAt": updatedAt, "state": "OPEN", "comments": map[string]any{
					"totalCount": max(1, s.commentCount), "nodes": []any{map[string]any{
						"updatedAt": s.commentEdit, "isMinimized": s.issueMinimized, "minimizedReason": "off-topic",
					}},
				}}
			}
			data[alias] = map[string]any{kind: pr}
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
		name        string
		interval    time.Duration
		spent       int
		firstReads  int64
		owners, prs int
	}{
		{"reported watch list", 5 * time.Minute, 0, 1, 16, 57},
		// Leave 63 paced units: two reads and their probes cost 44.
		// Three reads cost 60 and would fit if probe costs were ignored.
		{"sync interval provides headroom", 10 * time.Minute, 12, 2, 16, 57},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			budget := NewSyncBudgetWithEssentialReserve(500)
			budget.Spend(tc.spent)
			now := budget.windowStart
			budget.now = func() time.Time { return now }
			var repos []RepoRef
			var watched []WatchedMR
			for i := range tc.owners {
				owner := fmt.Sprintf("owner-%d", i)
				repos = append(repos, RepoRef{Owner: owner, Name: "app", PlatformHost: "github.com"})
				_, err := reposeed.Seed(t.Context(), d, verifiedGitHubRepoIdentity("github.com", owner, "app"))
				require.NoError(t, err)
			}
			for i := range tc.prs {
				watched = append(watched, WatchedMR{Owner: repos[i%tc.owners].Owner, Name: "app", Number: i + 1})
			}
			gql := &freshnessGraphQLServer{commentEdit: "2024-06-01T12:00:00Z", mergeState: "CLEAN", baseSHA: "base-1"}
			server := httptest.NewServer(gql.handler(t))
			t.Cleanup(server.Close)
			var reads int64
			refreshed := make(map[int]bool)
			client := &mockClient{getPullRequestFn: func(ctx context.Context, _, _ string, number int) (*gh.PullRequest, error) {
				_, admitted, _ := budget.trySpendForTransport(4, syncSpendKind(ctx), false)
				require.True(t, admitted)
				reads++
				refreshed[number] = true
				return buildOpenPR(number, now), nil
			}}
			syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil,
				repos, tc.interval, nil, map[string]*SyncBudget{"github.com": budget})
			t.Cleanup(syncer.Stop)
			syncer.SetFetchers(map[string]*GraphQLFetcher{
				"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, budget,
					WithGraphQLEndpointForTesting(server.URL)),
			})
			syncer.SetClock(func() time.Time { return now })
			syncer.SetWatchedMRs(watched)
			for tick := range 120 {
				gql.set(func(g *freshnessGraphQLServer) { g.commentEdit = now.Format(time.RFC3339) })
				syncer.syncWatchedMRs(t.Context())
				if tick == 0 {
					assert.Equal(t, tc.firstReads, gql.probes.Load())
					assert.Equal(t, tc.firstReads, gql.probedPRs.Load(), "probe only PRs this pass can read")
					assert.Equal(t, tc.firstReads, reads)
				}
				now = now.Add(30 * time.Second)
			}
			assert.Len(t, refreshed, tc.prs, "every watched PR must receive a full read within one hour")
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

// drainFreshnessClient reuses the conditional parent fixtures for both item kinds.
type drainFreshnessClient struct {
	conditionalPRTrackingClient
	issue       conditionalIssueTrackingClient
	threadReads atomic.Int64
}

func (c *drainFreshnessClient) GetIssueIfChanged(ctx context.Context, owner, repo string, number int, etag string) (*gh.Issue, string, bool, error) {
	return c.issue.GetIssueIfChanged(ctx, owner, repo, number, etag)
}

func (c *drainFreshnessClient) ListPullRequestReviewThreads(ctx context.Context, owner, repo string, number int) ([]platformgithub.PullRequestReviewThread, error) {
	c.threadReads.Add(1)
	return c.mockClient.ListPullRequestReviewThreads(ctx, owner, repo, number)
}

type drainFreshnessHarness struct {
	syncer *Syncer
	gql    *freshnessGraphQLServer
	client *drainFreshnessClient
	repoID int64
	bucket string
}

func newDrainFreshnessHarness(t *testing.T) *drainFreshnessHarness {
	t.Helper()
	ctx := t.Context()
	d := openTestDB(t)
	repo := RepoRef{Owner: "acme", Name: "app", Platform: platform.KindGitHub, PlatformHost: "github.com"}
	repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", "acme", "app"))
	require.NoError(t, err)
	updated := time.Now().UTC().Add(-6 * time.Hour)
	fetched := time.Now().UTC().Add(-31 * time.Minute)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 7, Number: 7, State: "open", MergeableState: "clean",
		CreatedAt: updated, UpdatedAt: updated, LastActivityAt: updated, DetailFetchedAt: &fetched,
	})
	require.NoError(t, err)
	_, err = d.UpsertIssue(ctx, &db.Issue{
		RepoID: repoID, PlatformID: 8, Number: 8, State: "open",
		CreatedAt: updated, UpdatedAt: updated, DetailFetchedAt: &fetched,
	})
	require.NoError(t, err)
	h := &drainFreshnessHarness{
		gql:    &freshnessGraphQLServer{mergeState: "CLEAN", commentEdit: "2024-06-01T12:00:00Z", threadEdit: "2024-06-01T12:00:00Z"},
		client: &drainFreshnessClient{}, repoID: repoID,
	}
	h.client.notModified = true
	h.client.issue.notModified = true
	h.syncer = NewSyncer(map[string]Client{"github.com": h.client}, d, nil, []RepoRef{repo}, time.Hour, nil, testBudget(10000))
	t.Cleanup(h.syncer.Stop)
	server := httptest.NewServer(h.gql.handler(t))
	t.Cleanup(server.Close)
	h.syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil, WithGraphQLEndpointForTesting(server.URL)),
	})
	h.bucket, err = h.syncer.bucketKeyForRepo(repo, false)
	require.NoError(t, err)
	return h
}

func (h *drainFreshnessHarness) drain(t *testing.T) {
	t.Helper()
	// Age the database markers without expiring the in-memory full-read baseline.
	for _, table := range []string{"forge_merge_requests", "forge_issues"} {
		_, err := h.syncer.db.WriteDB().ExecContext(t.Context(), "UPDATE "+table+" SET detail_fetched_at = datetime('now', '-31 minutes')")
		require.NoError(t, err)
	}
	h.syncer.drainDetailQueue(t.Context(), map[string]bool{h.bucket: true}, h.syncer.TrackedRepos())
}

func TestDetailDrainSkipsChildReadsForUnchangedActiveItems(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	h := newDrainFreshnessHarness(t)
	h.drain(t)
	assert.Equal(int64(2), h.gql.visibilityReads.Load())
	assert.Equal(int64(1), h.client.threadReads.Load())
	probes := h.gql.probes.Load()
	h.drain(t)
	assert.Equal(probes+1, h.gql.probes.Load(), "PR and issue share one probe")
	assert.Equal(int64(2), h.gql.visibilityReads.Load())
	assert.Equal(int64(1), h.client.threadReads.Load())
	assert.Equal(int32(1), h.client.conditionalCalls.Load())
	assert.Equal(int32(1), h.client.issue.conditionalCalls.Load())
	mr, err := h.syncer.db.GetMergeRequestByRepoIDAndNumber(t.Context(), h.repoID, 7)
	require.NoError(err)
	issue, err := h.syncer.db.GetIssueByRepoIDAndNumber(t.Context(), h.repoID, 8)
	require.NoError(err)
	require.NotNil(mr.DetailFetchedAt)
	require.NotNil(issue.DetailFetchedAt)
	assert.WithinDuration(time.Now(), *mr.DetailFetchedAt, 5*time.Second)
	assert.WithinDuration(time.Now(), *issue.DetailFetchedAt, 5*time.Second)

	// The drain's day-long baseline must still force a daily full read.
	h.syncer.SetClock(func() time.Time { return time.Now().Add(dailyRefetchInterval) })
	h.drain(t)
	assert.Equal(int64(4), h.gql.visibilityReads.Load())
	assert.Equal(int64(2), h.client.threadReads.Load())
}

func TestDetailDrainProbeSeesReviewThreadCommentEdit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*freshnessGraphQLServer)
		issue  bool
	}{
		{"thread comment edit", func(g *freshnessGraphQLServer) { g.threadEdit = "2024-06-01T13:00:00Z" }, false},
		{"resolved thread", func(g *freshnessGraphQLServer) { g.resolved = true }, false},
		{"minimized review comment", func(g *freshnessGraphQLServer) { g.reviewMinimized = true }, false},
		{"review decision", func(g *freshnessGraphQLServer) { g.reviewDecision = "REVIEW_REQUIRED" }, false},
		{"minimized issue comment", func(g *freshnessGraphQLServer) { g.issueMinimized = true }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDrainFreshnessHarness(t)
			h.drain(t)
			h.gql.set(tc.change)
			h.drain(t)
			assert.Equal(t, int64(3), h.gql.visibilityReads.Load(), "only changed item reads children again")
			threads := int64(2)
			if tc.issue {
				threads = 1
			}
			assert.Equal(t, threads, h.client.threadReads.Load())
			if tc.name == "review decision" {
				mr, err := h.syncer.db.GetMergeRequestByRepoIDAndNumber(t.Context(), h.repoID, 7)
				require.NoError(t, err)
				require.NotNil(t, mr)
				assert.Equal(t, "review_required", mr.ReviewDecision)
			}
			h.drain(t)
			assert.Equal(t, int64(3), h.gql.visibilityReads.Load(), "successful read records the changed fingerprint")
		})
	}
}

func TestDetailDrainRecordsBaselineForChangedItems(t *testing.T) {
	h := newDrainFreshnessHarness(t)
	for _, table := range []string{"forge_merge_requests", "forge_issues"} {
		_, err := h.syncer.db.WriteDB().ExecContext(t.Context(), "UPDATE "+table+" SET updated_at = datetime('now', '-1 minute')")
		require.NoError(t, err)
	}
	h.drain(t)
	assert.Equal(t, int64(2), h.gql.probedPRs.Load(), "changed items need a pre-read fingerprint")
	assert.Equal(t, int64(2), h.gql.visibilityReads.Load())
	h.drain(t)
	assert.Equal(t, int64(2), h.gql.visibilityReads.Load(), "the next unchanged pass uses the completed read's baseline")
	assert.Equal(t, int32(1), h.client.conditionalCalls.Load())
	assert.Equal(t, int32(1), h.client.issue.conditionalCalls.Load())
}

func TestDetailDrainProbesOnlyAdmittedItems(t *testing.T) {
	for _, mode := range []string{"exhausted", "paced", "one item", "one paced item"} {
		t.Run(mode, func(t *testing.T) {
			h := newDrainFreshnessHarness(t)
			budget := h.syncer.budgets[h.bucket]
			switch mode {
			case "exhausted":
				budget.SetLimit(0)
			case "paced":
				budget.now = func() time.Time { return budget.windowStart }
				budget.setPaceLead(minOptionalPaceLead)
				_, ok := budget.TrySpend(1000)
				require.True(t, ok)
				require.True(t, budget.CanSpend(PRDetailWorstCase))
				require.False(t, budget.CanSpendPaced(1))
			case "one item":
				budget.SetLimit(detailWorstCaseAttemptCost(platform.KindGitHub, QueueItemPR))
			case "one paced item":
				budget.now = func() time.Time { return budget.windowStart }
				budget.setPaceLead(minOptionalPaceLead)
				budget.SetLimit(12 * detailWorstCaseAttemptCost(platform.KindGitHub, QueueItemPR))
			}
			h.drain(t)
			want := int64(0)
			if mode == "one item" || mode == "one paced item" {
				want = 1
			}
			assert.Equal(t, want, h.gql.probedPRs.Load())
		})
	}
}

func TestDetailDrainOverflowNeverVouches(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(*freshnessGraphQLServer)
		visibility int64
	}{
		{"101 comments", func(g *freshnessGraphQLServer) { g.commentCount = 101 }, 4},
		{"26 threads", func(g *freshnessGraphQLServer) { g.threadCount = 26 }, 3},
		{"101 thread comments", func(g *freshnessGraphQLServer) { g.threadCommentCount = 101 }, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDrainFreshnessHarness(t)
			h.gql.set(tc.change)
			h.drain(t)
			h.drain(t)
			assert.Equal(t, tc.visibility, h.gql.visibilityReads.Load())
			assert.Equal(t, int64(2), h.client.threadReads.Load())
		})
	}
}

func TestFailedIssueVisibilityReadCannotVouchForTheProbe(t *testing.T) {
	h := newDrainFreshnessHarness(t)
	h.gql.set(func(g *freshnessGraphQLServer) { g.failIssueVisibility = true })
	h.drain(t)
	h.gql.set(func(g *freshnessGraphQLServer) { g.failIssueVisibility = false })
	h.drain(t)
	assert.Equal(t, int32(2), h.client.issue.conditionalCalls.Load(), "failed child read cannot vouch")
	h.drain(t)
	assert.Equal(t, int32(2), h.client.issue.conditionalCalls.Load(), "successful retry vouches")
}

func TestFreshnessProbeBatchesIssuesWithPullRequests(t *testing.T) {
	gql := &freshnessGraphQLServer{mergeState: "CLEAN"}
	server := httptest.NewServer(gql.handler(t))
	t.Cleanup(server.Close)
	fetcher := NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, nil, WithGraphQLEndpointForTesting(server.URL))
	var refs []PullRequestFreshnessRef
	for i := range 50 {
		refs = append(refs, PullRequestFreshnessRef{Owner: "acme", Name: "app", Number: i + 1, IsIssue: i >= 30})
	}
	fingerprints, err := fetcher.fetchFreshness(t.Context(), refs, prCompleteFreshnessFields, true)
	require.NoError(t, err)
	assert.Len(t, fingerprints, 50)
	assert.Equal(t, int64(2), gql.probes.Load())
	for _, ref := range refs {
		assert.NotEmpty(t, fingerprints[ref].Fingerprint)
	}
}
