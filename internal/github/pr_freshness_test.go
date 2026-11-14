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
	commentSelection         = regexp.MustCompile(`comments\([^)]*\)\s*\{([^{}]*)nodes\s*\{([^}]*)\}`)
	threadSelection          = regexp.MustCompile(`reviewThreads\([^)]*\)\s*\{([^{}]*)nodes\s*\{([^{}]*)`)
	threadCommentSelection   = regexp.MustCompile(`(?s)reviewThreads\([^)]*\)\s*\{.*?comments\([^)]*\)\s*\{([^{}]*)nodes\s*\{([^}]*)\}`)
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
	// onVisibility runs after a full comment visibility request.
	onVisibility func()
	// failMergeQueue answers merge queue reads with a GraphQL error.
	failMergeQueue bool
}

func (s *freshnessGraphQLServer) set(update func(*freshnessGraphQLServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(s)
}

func (s *freshnessGraphQLServer) handler(t *testing.T) http.Handler {
	t.Helper()

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
				if s.onVisibility != nil {
					s.onVisibility()
				}
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
			comments := map[string]any{"nodes": []any{comment}}
			if selection := commentSelection.FindStringSubmatch(req.Query); len(selection) > 0 && strings.Contains(selection[1], "totalCount") {
				comments["totalCount"] = max(1, s.commentCount)
			}
			pr["comments"] = comments
			if strings.Contains(req.Query, "reviewDecision") {
				pr["reviewDecision"] = s.reviewDecision
			}
			thread := map[string]any{}
			if selection := threadSelection.FindStringSubmatch(req.Query); len(selection) > 0 {
				for field, value := range map[string]any{
					"isResolved": s.resolved, "isOutdated": false, "path": "main.go", "line": 7,
				} {
					if strings.Contains(selection[2], field) {
						thread[field] = value
					}
				}
				threads := map[string]any{"nodes": []any{thread}}
				if strings.Contains(selection[1], "totalCount") {
					threads["totalCount"] = max(1, s.threadCount)
				}
				pr["reviewThreads"] = threads
			}
			if selection := threadCommentSelection.FindStringSubmatch(req.Query); len(selection) > 0 {
				comment := map[string]any{}
				if strings.Contains(selection[2], "updatedAt") {
					comment["updatedAt"] = s.threadEdit
				}
				if strings.Contains(selection[2], "isMinimized") {
					comment["isMinimized"] = s.reviewMinimized
				}
				if strings.Contains(selection[2], "minimizedReason") {
					comment["minimizedReason"] = "off-topic"
				}
				comments := map[string]any{"nodes": []any{comment}}
				if strings.Contains(selection[1], "totalCount") {
					comments["totalCount"] = max(1, s.threadCommentCount)
				}
				thread["comments"] = comments
			}
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
				_, fragment, _ := strings.Cut(req.Query, "fragment IssueFreshness on Issue {")
				pr = map[string]any{"updatedAt": updatedAt, "state": "OPEN"}
				if selection := commentSelection.FindStringSubmatch(fragment); len(selection) > 0 {
					comment := map[string]any{}
					for field, value := range map[string]any{
						"updatedAt": s.commentEdit, "isMinimized": s.issueMinimized, "minimizedReason": "off-topic",
					} {
						if strings.Contains(selection[2], field) {
							comment[field] = value
						}
					}
					comments := map[string]any{"nodes": []any{comment}}
					if strings.Contains(selection[1], "totalCount") {
						comments["totalCount"] = max(1, s.commentCount)
					}
					pr["comments"] = comments
				}
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
		refs = append(refs, PullRequestFreshnessRef{Owner: "acme", Name: "app", Number: i + 1, IsIssue: i >= 30})
	}
	fingerprints, err := fetcher.fetchFreshness(t.Context(), refs, prCompleteFreshnessFields, true)
	require.NoError(err)
	assert.Len(fingerprints, 60)
	assert.Equal(int64(3), gql.probes.Load(), "60 items fit in 3 requests of 25")
	assert.Equal(int64(60), gql.probedPRs.Load())
	for _, ref := range refs {
		assert.NotEmpty(fingerprints[ref].Fingerprint)
	}
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
	h.syncer.drainDetailQueue(WithSyncBudget(t.Context()), map[string]bool{h.bucket: true}, h.syncer.TrackedRepos())
}

func TestDetailDrainSkipsChildReadsForUnchangedActiveItems(t *testing.T) {
	for _, nearCeiling := range []bool{false, true} {
		name := "ample budget"
		if nearCeiling {
			name = "near budget ceiling"
		}
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			h := newDrainFreshnessHarness(t)
			h.drain(t)
			assert.Equal(int64(2), h.gql.visibilityReads.Load())
			assert.Equal(int64(1), h.client.threadReads.Load())
			assert.Equal(int64(2), h.gql.probedPRs.Load(), "both items need a pre-read fingerprint")
			if nearCeiling {
				budget := NewSyncBudget(100)
				now := budget.windowStart.Add(50 * time.Minute)
				budget.now = func() time.Time { return now }
				budget.Spend(80)
				h.syncer.hosts().budgets[h.bucket] = budget
				server := httptest.NewServer(h.gql.handler(t))
				t.Cleanup(server.Close)
				h.syncer.SetFetchers(map[string]*GraphQLFetcher{
					"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, budget,
						WithGraphQLEndpointForTesting(server.URL)),
				})
			}
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

			if !nearCeiling {
				// The drain's day-long baseline must still force a daily full read.
				h.syncer.SetClock(func() time.Time { return time.Now().Add(dailyRefetchInterval) })
				h.drain(t)
				assert.Equal(int64(4), h.gql.visibilityReads.Load())
				assert.Equal(int64(2), h.client.threadReads.Load())
			}
		})
	}
}

func TestDetailDrainMatchingProbeHonorsRESTReserve(t *testing.T) {
	for _, remaining := range []int{RateReserveBuffer, 0} {
		t.Run(fmt.Sprintf("remaining=%d", remaining), func(t *testing.T) {
			h := newDrainFreshnessHarness(t)
			_, err := h.syncer.db.WriteDB().ExecContext(t.Context(),
				"UPDATE forge_merge_requests SET platform_head_sha = 'head'")
			require.NoError(t, err)
			h.drain(t)
			ciReads := h.client.getCombinedCalls.Load()

			registry := NewQuotaRegistry()
			h.syncer.SetQuotaRegistry(registry)
			h.gql.set(func(g *freshnessGraphQLServer) {
				g.onProbe = func() {
					registry.UpdateSnapshot(HostIdentity("github.com"), QuotaResourceREST, Rate{
						Limit: 5000, Remaining: remaining, Reset: time.Now().UTC().Add(time.Hour),
					})
					h.syncer.invalidateReserveVerdicts(h.bucket)
				}
			})
			var identityReads atomic.Int64
			h.client.getRepositoryByIDFn = func(_ context.Context, owner string, id int64) (*gh.Repository, error) {
				identityReads.Add(1)
				return &gh.Repository{ID: &id, Name: new("app"), Owner: &gh.User{Login: &owner}}, nil
			}

			h.drain(t)
			assert.Equal(t, int64(2), h.gql.probes.Load(), "a matching probe ran before REST quota fell")
			assert.Zero(t, identityReads.Load(), "the reserve must gate repository verification")
			assert.Equal(t, ciReads, h.client.getCombinedCalls.Load(), "the reserve must gate REST CI reads")
			mr, err := h.syncer.db.GetMergeRequestByRepoIDAndNumber(t.Context(), h.repoID, 7)
			require.NoError(t, err)
			require.NotNil(t, mr.DetailFetchedAt)
			assert.Greater(t, time.Since(*mr.DetailFetchedAt), 30*time.Minute, "the PR must stay due")
		})
	}
}

func TestDetailDrainSkipsProbesForDormantDailyItems(t *testing.T) {
	h := newDrainFreshnessHarness(t)
	for _, table := range []string{"forge_merge_requests", "forge_issues"} {
		_, err := h.syncer.db.WriteDB().ExecContext(t.Context(), "UPDATE "+table+" SET updated_at = datetime('now', '-3 days'), detail_fetched_at = datetime('now', '-25 hours')")
		require.NoError(t, err)
	}

	h.syncer.drainDetailQueue(WithSyncBudget(t.Context()), map[string]bool{h.bucket: true}, h.syncer.TrackedRepos())
	assert.Zero(t, h.gql.probes.Load(), "daily full reads cannot reuse a probe baseline")
	assert.Equal(t, int64(2), h.gql.visibilityReads.Load(), "daily items still refresh children")
	assert.Equal(t, int64(1), h.client.threadReads.Load())
}

func TestDetailDrainProbePacing(t *testing.T) {
	for _, tc := range []struct {
		name       string
		spent      bool
		probes     int64
		probedRefs int64
	}{
		{"spent pacing", true, 0, 0},
		{"leave pace for details", false, 1, 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDrainFreshnessHarness(t)
			budget := h.syncer.hosts().budgets[h.bucket]
			if !tc.spent {
				updated := time.Now().UTC().Add(-6 * time.Hour)
				fetched := time.Now().UTC().Add(-31 * time.Minute)
				for number := 9; number < 83; number++ {
					_, err := h.syncer.db.UpsertIssue(t.Context(), &db.Issue{
						RepoID: h.repoID, PlatformID: int64(number), Number: number, State: "open",
						CreatedAt: updated, UpdatedAt: updated, DetailFetchedAt: &fetched,
					})
					require.NoError(t, err)
				}
				budget = NewSyncBudget(50)
				h.syncer.hosts().budgets[h.bucket] = budget
			}
			now := budget.windowStart
			budget.now = func() time.Time { return now }
			if tc.spent {
				for budget.CanSpendPaced(1) {
					_, admitted, _ := budget.trySpendForTransport(1, SyncSpendDetail, false)
					require.True(t, admitted)
				}
				require.True(t, budget.CanSpend(detailWorstCaseAttemptCost(platform.KindGitHub, QueueItemPR)), "flat ceiling still permits detail work")
			} else {
				require.True(t, budget.CanSpendPaced(4))
				require.False(t, budget.CanSpendPaced(5))
			}
			server := httptest.NewServer(h.gql.handler(t))
			t.Cleanup(server.Close)
			h.syncer.SetFetchers(map[string]*GraphQLFetcher{
				"github.com": NewGraphQLFetcher(testTokenSource("token"), "github.com", nil, budget,
					WithGraphQLEndpointForTesting(server.URL)),
			})

			h.drain(t)
			assert.Equal(t, tc.probes, h.gql.probes.Load(), "probe batches must leave pace for the detail loop")
			assert.Equal(t, tc.probedRefs, h.gql.probedPRs.Load(), "admit the rest of a batch without charging another request")
			if !tc.spent {
				assert.Positive(t, h.gql.visibilityReads.Load(), "metered probes must leave room to refresh children")
				assert.Positive(t, h.client.conditionalCalls.Load()+h.client.issue.conditionalCalls.Load(), "details must run")
			}
		})
	}
}

func TestDetailDrainProbeSeesChildChanges(t *testing.T) {
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

			h.drain(t)
			assert.Equal(t, int64(3), h.gql.visibilityReads.Load(), "successful read records the changed fingerprint")
		})
	}
}

func TestDetailDrainRetriesChildrenAfterGraphQLReserveRecovers(t *testing.T) {
	for _, skipAfter := range []string{"probe", "PR visibility"} {
		t.Run(skipAfter, func(t *testing.T) {
			assert := assert.New(t)
			h := newDrainFreshnessHarness(t)
			registry := NewQuotaRegistry()
			h.syncer.SetQuotaRegistry(registry)
			identity := HostIdentity("github.com")
			reset := time.Now().UTC().Add(time.Hour)
			updateQuota := func(remaining int) {
				registry.UpdateSnapshot(identity, QuotaResourceGraphQL, Rate{
					Limit: 5000, Remaining: remaining, Reset: reset,
				})
				// A newly published rate snapshot invalidates the cached verdict.
				h.syncer.invalidateReserveVerdicts(h.bucket)
			}
			updateQuota(5000)
			h.gql.set(func(g *freshnessGraphQLServer) {
				if skipAfter == "probe" {
					g.onProbe = func() { updateQuota(RateReserveBuffer / 2) }
				} else {
					g.onVisibility = func() { updateQuota(RateReserveBuffer / 2) }
				}
			})
			h.drain(t)
			assert.Equal(int64(1), h.gql.probes.Load(), "the probe must run before the quota drop")
			assert.Zero(h.client.threadReads.Load(), "the PR thread refresh must skip inside the reserve")
			visibilityReads := int64(0)
			if skipAfter == "PR visibility" {
				visibilityReads = 1
			}
			assert.Equal(visibilityReads, h.gql.visibilityReads.Load())

			h.gql.set(func(g *freshnessGraphQLServer) {
				g.onProbe = nil
				g.onVisibility = nil
			})
			updateQuota(5000)
			h.drain(t)
			assert.Equal(visibilityReads+2, h.gql.visibilityReads.Load(), "quota recovery must refresh both items' children")
			assert.Equal(int64(1), h.client.threadReads.Load())
			h.drain(t)
			assert.Equal(visibilityReads+2, h.gql.visibilityReads.Load(), "the completed reads can now vouch")
			assert.Equal(int64(1), h.client.threadReads.Load())
		})
	}
}

func TestDetailDrainOverflowNeverVouches(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(*freshnessGraphQLServer)
		visibility int64
		probed     int64
	}{
		{"101 comments", func(g *freshnessGraphQLServer) { g.commentCount = 101 }, 6, 2},
		{"26 threads", func(g *freshnessGraphQLServer) { g.threadCount = 26 }, 4, 4},
		{"101 thread comments", func(g *freshnessGraphQLServer) { g.threadCommentCount = 101 }, 4, 4},
		{"unknown mergeability", func(g *freshnessGraphQLServer) { g.mergeState = "UNKNOWN" }, 4, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			h := newDrainFreshnessHarness(t)
			h.gql.set(tc.change)
			h.drain(t)
			h.drain(t)
			h.drain(t)
			assert.Equal(tc.visibility, h.gql.visibilityReads.Load())
			assert.Equal(int64(3), h.client.threadReads.Load(), "each round still reads the PR in full")
			assert.Equal(tc.probed, h.gql.probedPRs.Load(), "only overflow suppresses later probes")

			h.syncer.SetClock(func() time.Time { return time.Now().Add(dailyRefetchInterval) })
			h.drain(t)
			assert.Equal(tc.probed+2, h.gql.probedPRs.Load(), "overflow expires with the day-long baseline window")
			assert.Equal(int64(4), h.client.threadReads.Load())
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
