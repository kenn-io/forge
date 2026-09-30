package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

// These tests pin when sync records a merge status observation. The fake
// providers advance the clock by one minute while serving each request, so a
// time captured before a request differs from one captured after it:
// requests are sent at 10:00, 10:01, 10:02, ... in call order.

const (
	seededAt   = "2026-08-01T09:00:00Z"
	sentAt1000 = "2026-09-01T10:00:00Z"
	sentAt1001 = "2026-09-01T10:01:00Z"
	sentAt1002 = "2026-09-01T10:02:00Z"
	seededHead = "abc123def456"
	seededBase = "base111"
)

type observationClock struct {
	mu  sync.Mutex
	now time.Time
}

func newObservationClock() *observationClock {
	return &observationClock{now: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}
}

func (c *observationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *observationClock) advance() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Minute)
}

// mergeStatus is the stored merge status of one merge request, with each
// observation time rendered as RFC 3339 ("" when NULL).
type mergeStatus struct {
	Review      string
	ReviewAt    string
	CI          string
	CIAt        string
	Mergeable   string
	MergeableAt string
}

func observedAtString(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

func toMergeStatus(mr *db.MergeRequest) mergeStatus {
	return mergeStatus{
		Review: mr.ReviewDecision, ReviewAt: observedAtString(mr.ReviewDecisionObservedAt),
		CI: mr.CIStatus, CIAt: observedAtString(mr.CIObservedAt),
		Mergeable: mr.MergeableState, MergeableAt: observedAtString(mr.MergeableStateObservedAt),
	}
}

func readMergeStatus(t *testing.T, d *db.DB, repoID int64) mergeStatus {
	t.Helper()
	mr, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(t, err)
	require.NotNil(t, mr)
	return toMergeStatus(mr)
}

// seededStatus is what seedObservedMR stores for a GitHub merge request.
var seededStatus = mergeStatus{
	Review: "changes_requested", ReviewAt: seededAt,
	CI: "failure", CIAt: seededAt,
	Mergeable: "dirty", MergeableAt: seededAt,
}

// seedObservedMR stores merge request #1 on the seeded head with a failing CI
// run, a dirty mergeable state, and (unless review is empty) a review
// decision, all observed at seededAt.
func seedObservedMR(t *testing.T, d *db.DB, repoID int64, review string, ciHadPending bool) {
	t.Helper()
	at := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	mr := &db.MergeRequest{
		RepoID: repoID, PlatformID: 1000, Number: 1,
		URL: "https://example.com/example/project-a/pull/1", Title: "test PR", Author: "user-a",
		State: "open", HeadBranch: "feature-branch", BaseBranch: "main",
		PlatformHeadSHA: seededHead, PlatformBaseSHA: seededBase,
		CIStatus:     "failure",
		CIChecksJSON: `[{"name":"seeded","status":"completed","conclusion":"failure"}]`,
		CIObservedAt: &at, CIHadPending: ciHadPending,
		MergeableState: "dirty", MergeableStateObservedAt: &at,
		CreatedAt: at, UpdatedAt: at, LastActivityAt: at,
	}
	if review != "" {
		mr.ReviewDecision = review
		mr.ReviewDecisionObservedAt = &at
	}
	_, err := d.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

// observingGitHubClient advances the clock while serving each provider
// request whose response carries merge status.
type observingGitHubClient struct {
	*mockClient
	clock       *observationClock
	notModified bool
}

func (c *observingGitHubClient) ListOpenPullRequests(
	ctx context.Context, owner, repo string,
) ([]*gh.PullRequest, error) {
	defer c.clock.advance()
	return c.mockClient.ListOpenPullRequests(ctx, owner, repo)
}

func (c *observingGitHubClient) GetPullRequest(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	defer c.clock.advance()
	return c.mockClient.GetPullRequest(ctx, owner, repo, number)
}

func (c *observingGitHubClient) GetPullRequestIfChanged(
	ctx context.Context, owner, repo string, number int, etag string,
) (*gh.PullRequest, string, bool, error) {
	if c.notModified {
		c.clock.advance()
		return nil, etag, true, nil
	}
	pr, err := c.GetPullRequest(ctx, owner, repo, number)
	return pr, etag, false, err
}

func (c *observingGitHubClient) ListReviews(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.PullRequestReview, error) {
	defer c.clock.advance()
	return c.mockClient.ListReviews(ctx, owner, repo, number)
}

func (c *observingGitHubClient) ListCheckRunsForRef(
	ctx context.Context, owner, repo, ref string,
) ([]*gh.CheckRun, error) {
	defer c.clock.advance()
	return c.mockClient.ListCheckRunsForRef(ctx, owner, repo, ref)
}

// observingReadProvider is a non-GitHub provider that advances the clock
// while serving each request whose response carries merge status.
type observingReadProvider struct {
	*syncTestReadProvider
	clock  *observationClock
	checks []platform.CICheck
}

func (p *observingReadProvider) ListOpenMergeRequests(
	ctx context.Context, ref platform.RepoRef,
) ([]platform.MergeRequest, error) {
	defer p.clock.advance()
	return p.syncTestReadProvider.ListOpenMergeRequests(ctx, ref)
}

func (p *observingReadProvider) GetMergeRequest(
	ctx context.Context, ref platform.RepoRef, number int,
) (platform.MergeRequest, error) {
	defer p.clock.advance()
	return p.syncTestReadProvider.GetMergeRequest(ctx, ref, number)
}

func (p *observingReadProvider) ListCIChecks(
	context.Context, platform.RepoRef, string,
) ([]platform.CICheck, error) {
	defer p.clock.advance()
	return p.checks, nil
}

var observedGitHubRepo = RepoRef{Owner: "example", Name: "project-a", PlatformHost: "github.com"}

// newObservedGitHubSyncer seeds the repository and returns a syncer whose
// provider serves pr, one approving review, and one successful check run.
func newObservedGitHubSyncer(
	t *testing.T, pr *gh.PullRequest,
) (*Syncer, *observingGitHubClient, *db.DB, int64) {
	t.Helper()
	d := openTestDB(t)
	repoID, err := reposeed.Seed(t.Context(), d, verifiedGitHubRepoIdentity("github.com", "example", "project-a"))
	require.NoError(t, err)
	clock := newObservationClock()
	client := &observingGitHubClient{clock: clock, mockClient: &mockClient{
		openPRs:  []*gh.PullRequest{pr},
		singlePR: pr,
		reviews: []*gh.PullRequestReview{{
			User: &gh.User{Login: new("user-b")}, State: new("APPROVED"),
		}},
		checkRuns: []*gh.CheckRun{{
			Name: new("tests"), Status: new("completed"), Conclusion: new("success"),
		}},
	}}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, d, nil,
		[]RepoRef{observedGitHubRepo}, time.Minute, nil, nil,
	)
	syncer.SetClock(clock.Now)
	return syncer, client, d, repoID
}

// observedPR is PR #1 updated after the seeded row, on head, with the given
// REST mergeable state.
func observedPR(head, mergeable string) *gh.PullRequest {
	pr := buildOpenPRWithSHA(1, time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC), head)
	pr.Base.SHA = new(seededBase)
	if mergeable != "" {
		pr.MergeableState = new(mergeable)
	}
	return pr
}

var observedGitLabRepo = RepoRef{
	Platform: platform.KindGitLab, PlatformHost: "gitlab.example.com",
	Owner: "example", Name: "project-a", RepoPath: "example/project-a",
}

func newObservedGitLabSyncer(
	t *testing.T, mr platform.MergeRequest,
) (*Syncer, *db.DB, int64) {
	t.Helper()
	d := openTestDB(t)
	repoID, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(observedGitLabRepo)))
	require.NoError(t, err)
	clock := newObservationClock()
	mr.Repo = platformRepoRef(observedGitLabRepo)
	provider := &observingReadProvider{
		syncTestReadProvider: &syncTestReadProvider{
			kind: platform.KindGitLab, host: "gitlab.example.com",
			mergeRequests: []platform.MergeRequest{mr},
		},
		clock:  clock,
		checks: []platform.CICheck{{Name: "tests", Status: "completed", Conclusion: "success"}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	syncer := NewSyncerWithRegistry(registry, d, nil, []RepoRef{observedGitLabRepo}, time.Minute, nil, nil)
	syncer.SetClock(clock.Now)
	return syncer, d, repoID
}

// observedGitLabMR is MR #1 updated after the seeded row.
func observedGitLabMR(head, mergeable string) platform.MergeRequest {
	at := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	return platform.MergeRequest{
		PlatformID: 1000, PlatformExternalID: "1000", Number: 1,
		URL: "https://gitlab.example.com/example/project-a/-/merge_requests/1", Title: "test MR",
		Author: "user-a", State: "open", HeadBranch: "feature-branch", BaseBranch: "main",
		HeadSHA: head, BaseSHA: seededBase, MergeableState: mergeable,
		CreatedAt: at, UpdatedAt: at, LastActivityAt: at,
	}
}

func TestListSyncCarriesGitHubMergeStatusTimes(t *testing.T) {
	entries := map[string]func(*Syncer, int64) error{
		"provider list": func(s *Syncer, repoID int64) error {
			return s.indexSyncRepo(t.Context(), observedGitHubRepo, repoID, false)
		},
		"relay refs": func(s *Syncer, _ int64) error {
			return s.refreshRelayRefs(t.Context(), observedGitHubRepo)
		},
	}
	tests := []struct {
		name string
		head string
		want mergeStatus
	}{
		{name: "same head keeps stored values and times", head: seededHead, want: seededStatus},
		{name: "new head clears values and times", head: "newhead", want: mergeStatus{}},
	}
	for entryName, sync := range entries {
		for _, tt := range tests {
			t.Run(entryName+"/"+tt.name, func(t *testing.T) {
				syncer, _, d, repoID := newObservedGitHubSyncer(t, observedPR(tt.head, ""))
				seedObservedMR(t, d, repoID, "changes_requested", false)

				require.NoError(t, sync(syncer, repoID))

				assert.Equal(t, tt.want, readMergeStatus(t, d, repoID))
			})
		}
	}
}

func TestListSyncStampsGitLabMergeableStateAtListRequest(t *testing.T) {
	tests := []struct {
		name      string
		head      string
		mergeable string
		want      mergeStatus
	}{
		{
			name: "same head observes mergeable and keeps CI", head: seededHead, mergeable: "clean",
			want: mergeStatus{CI: "failure", CIAt: seededAt, Mergeable: "clean", MergeableAt: sentAt1000},
		},
		{
			name: "unknown mergeable keeps stored state", head: seededHead, mergeable: "unknown",
			want: mergeStatus{CI: "failure", CIAt: seededAt, Mergeable: "dirty", MergeableAt: seededAt},
		},
		{
			name: "new head observes mergeable and clears CI", head: "newhead", mergeable: "clean",
			want: mergeStatus{Mergeable: "clean", MergeableAt: sentAt1000},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, d, repoID := newObservedGitLabSyncer(t, observedGitLabMR(tt.head, tt.mergeable))
			seedObservedMR(t, d, repoID, "", false)

			require.NoError(t, syncer.indexSyncRepo(t.Context(), observedGitLabRepo, repoID, false))

			assert.Equal(t, tt.want, readMergeStatus(t, d, repoID))
		})
	}
}

// graphQLPRNode renders PR #1 as the bulk GraphQL query returns it.
func graphQLPRNode(head, mergeable, rollup string) string {
	return fmt.Sprintf(`{"databaseId":1000,"number":1,"title":"test PR","state":"OPEN",`+
		`"url":"https://github.com/example/project-a/pull/1","author":{"login":"user-a"},`+
		`"createdAt":"2026-09-01T09:30:00Z","updatedAt":"2026-09-01T09:30:00Z",`+
		`"mergeable":%q,"reviewDecision":"APPROVED","headRefName":"feature-branch",`+
		`"baseRefName":"main","headRefOid":%q,"baseRefOid":%q,`+
		`"labels":{"nodes":[]},"assignees":{"nodes":[]},"reviewRequests":{"nodes":[]},`+
		`"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}},`+
		`"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}},`+
		`"reviews":{"nodes":[],"pageInfo":{"hasNextPage":false}},`+
		`"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":false}},`+
		`"lastCommit":{"nodes":[{"commit":{"statusCheckRollup":%s}}]},`+
		`"timelineItems":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`,
		mergeable, head, seededBase, rollup)
}

func TestGraphQLBulkSyncStampsMergeStatusAtBulkRequest(t *testing.T) {
	const passingRollup = `{"contexts":{"nodes":[{"__typename":"CheckRun","name":"tests",` +
		`"status":"COMPLETED","conclusion":"SUCCESS"}],"pageInfo":{"hasNextPage":false}}}`
	const truncatedRollup = `{"contexts":{"nodes":[{"__typename":"CheckRun","name":"tests",` +
		`"status":"COMPLETED","conclusion":"SUCCESS"}],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}`
	// The REST list request runs at 10:00; the bulk request is sent at 10:01.
	tests := []struct {
		name string
		node string
		want mergeStatus
	}{
		{
			name: "complete response observes every field",
			node: graphQLPRNode(seededHead, "MERGEABLE", passingRollup),
			want: mergeStatus{
				Review: "approved", ReviewAt: sentAt1001, CI: "success", CIAt: sentAt1001,
				Mergeable: "clean", MergeableAt: sentAt1001,
			},
		},
		{
			name: "unknown mergeable has no time and missing rollup observes no CI",
			node: graphQLPRNode(seededHead, "UNKNOWN", "null"),
			want: mergeStatus{
				Review: "approved", ReviewAt: sentAt1001, CI: "", CIAt: sentAt1001,
				Mergeable: "unknown", MergeableAt: "",
			},
		},
		{
			name: "truncated rollup keeps stored CI",
			node: graphQLPRNode(seededHead, "CONFLICTING", truncatedRollup),
			want: mergeStatus{
				Review: "approved", ReviewAt: sentAt1001, CI: "failure", CIAt: seededAt,
				Mergeable: "dirty", MergeableAt: sentAt1001,
			},
		},
		{
			name: "new head with truncated rollup clears CI",
			node: graphQLPRNode("newhead", "MERGEABLE", truncatedRollup),
			want: mergeStatus{
				Review: "approved", ReviewAt: sentAt1001, Mergeable: "clean", MergeableAt: sentAt1001,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, client, d, repoID := newObservedGitHubSyncer(t, observedPR(seededHead, ""))
			seedObservedMR(t, d, repoID, "changes_requested", false)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				if bytes.Contains(body, []byte("pullRequests(")) {
					client.clock.advance()
					_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"totalCount":1,`+
						`"nodes":[%s],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`, tt.node)
					return
				}
				_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],` +
					`"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			}))
			t.Cleanup(srv.Close)
			syncer.SetFetchers(map[string]*GraphQLFetcher{
				"github.com": NewGraphQLFetcherWithClient(
					githubv4.NewEnterpriseClient(srv.URL, srv.Client()), nil,
				),
			})

			require.NoError(t, syncer.indexSyncRepo(t.Context(), observedGitHubRepo, repoID, false))

			assert.Equal(t, tt.want, readMergeStatus(t, d, repoID))
		})
	}
}

func TestOnDemandGitHubSyncStampsEachFieldAtItsRequest(t *testing.T) {
	// PR fetch at 10:00, reviews at 10:01, check runs at 10:02.
	tests := []struct {
		name      string
		checksErr error
		want      mergeStatus
	}{
		{
			name: "every fetch succeeds",
			want: mergeStatus{
				Review: "approved", ReviewAt: sentAt1001, CI: "success", CIAt: sentAt1002,
				Mergeable: "clean", MergeableAt: sentAt1000,
			},
		},
		{
			name: "failed CI fetch keeps stored CI", checksErr: errors.New("check runs unavailable"),
			want: mergeStatus{
				Review: "approved", ReviewAt: sentAt1001, CI: "failure", CIAt: seededAt,
				Mergeable: "clean", MergeableAt: sentAt1000,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, client, d, repoID := newObservedGitHubSyncer(t, observedPR(seededHead, "clean"))
			client.checkRunsErr = tt.checksErr
			seedObservedMR(t, d, repoID, "changes_requested", false)

			require.NoError(t, syncer.SyncMR(t.Context(), "example", "project-a", 1))

			assert.Equal(t, tt.want, readMergeStatus(t, d, repoID))
		})
	}
}

func TestOnDemandGitLabSyncStampsMergeableAndCIAtTheirRequests(t *testing.T) {
	syncer, d, repoID := newObservedGitLabSyncer(t, observedGitLabMR(seededHead, "clean"))
	seedObservedMR(t, d, repoID, "", false)

	require.NoError(t, syncer.SyncMROnProvider(
		t.Context(), platform.KindGitLab, "gitlab.example.com", "example", "project-a", 1,
	))

	// MR fetch at 10:00, CI checks at 10:01.
	assert.Equal(t, mergeStatus{
		CI: "success", CIAt: sentAt1001, Mergeable: "clean", MergeableAt: sentAt1000,
	}, readMergeStatus(t, d, repoID))
}

func TestDetailDrainClearsThenReobservesGitHubReviewAndCI(t *testing.T) {
	// PR fetch at 10:00, reviews at 10:01, check runs at 10:02.
	tests := []struct {
		name      string
		checksErr error
		want      mergeStatus
	}{
		{
			name: "CI refresh succeeds",
			want: mergeStatus{
				Review: "approved", ReviewAt: sentAt1001, CI: "success", CIAt: sentAt1002,
				Mergeable: "clean", MergeableAt: sentAt1000,
			},
		},
		{
			name: "CI refresh fails", checksErr: errors.New("check runs unavailable"),
			want: mergeStatus{
				Review: "approved", ReviewAt: sentAt1001, Mergeable: "clean", MergeableAt: sentAt1000,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, client, d, repoID := newObservedGitHubSyncer(t, observedPR(seededHead, "clean"))
			client.checkRunsErr = tt.checksErr
			seedObservedMR(t, d, repoID, "changes_requested", false)
			var afterParent mergeStatus
			syncer.afterMergeRequestParentSnapshotCommit = func() {
				afterParent = readMergeStatus(t, d, repoID)
			}

			_, err := syncer.fetchMRDetail(t.Context(), observedGitHubRepo, repoID, 1, false)
			require.NoError(t, err)

			assert.Equal(t, mergeStatus{Mergeable: "clean", MergeableAt: sentAt1000}, afterParent)
			assert.Equal(t, tt.want, readMergeStatus(t, d, repoID))
		})
	}
}

func TestDetailDrainStampsGitLabCIAtChecksRequest(t *testing.T) {
	mr := observedGitLabMR(seededHead, "clean")
	mr.CIStatus = "running"
	syncer, d, repoID := newObservedGitLabSyncer(t, mr)
	seedObservedMR(t, d, repoID, "", false)
	var afterParent mergeStatus
	syncer.afterMergeRequestParentSnapshotCommit = func() {
		afterParent = readMergeStatus(t, d, repoID)
	}

	_, err := syncer.fetchMRDetail(t.Context(), observedGitLabRepo, repoID, 1, false)
	require.NoError(t, err)

	// MR fetch at 10:00, CI checks at 10:01. The pipeline-only status the
	// parent snapshot writes has no checks, so it is not an observation.
	assert.Equal(t, mergeStatus{CI: "running", Mergeable: "clean", MergeableAt: sentAt1000}, afterParent)
	assert.Equal(t, mergeStatus{
		CI: "success", CIAt: sentAt1001, Mergeable: "clean", MergeableAt: sentAt1000,
	}, readMergeStatus(t, d, repoID))
}

func TestUnchangedDetailKeepsReviewAndMergeableAndStampsPendingCI(t *testing.T) {
	syncer, client, d, repoID := newObservedGitHubSyncer(t, observedPR(seededHead, "clean"))
	client.notModified = true
	seedObservedMR(t, d, repoID, "changes_requested", true)

	_, err := syncer.fetchMRDetail(t.Context(), observedGitHubRepo, repoID, 1, false)
	require.NoError(t, err)

	// The 304 request is sent at 10:00 and the check runs at 10:01.
	assert.Equal(t, mergeStatus{
		Review: "changes_requested", ReviewAt: seededAt, CI: "success", CIAt: sentAt1001,
		Mergeable: "dirty", MergeableAt: seededAt,
	}, readMergeStatus(t, d, repoID))
}

func TestClosedRefetchCarriesReviewAndCIAndStampsMergeable(t *testing.T) {
	tests := []struct {
		name      string
		mergeable string
		want      mergeStatus
	}{
		{
			name: "concrete mergeable state", mergeable: "clean",
			want: mergeStatus{
				Review: "changes_requested", ReviewAt: seededAt, CI: "failure", CIAt: seededAt,
				Mergeable: "clean", MergeableAt: sentAt1000,
			},
		},
		{
			name: "unknown mergeable state", mergeable: "unknown",
			want: mergeStatus{
				Review: "changes_requested", ReviewAt: seededAt, CI: "failure", CIAt: seededAt,
				Mergeable: "unknown",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := observedPR(seededHead, tt.mergeable)
			pr.State = new("closed")
			pr.ClosedAt = makeTimestamp(time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC))
			syncer, _, d, repoID := newObservedGitHubSyncer(t, pr)
			seedObservedMR(t, d, repoID, "changes_requested", false)

			require.NoError(t, syncer.SyncClosedMROnProvider(t.Context(), repoID, 1))

			assert.Equal(t, tt.want, readMergeStatus(t, d, repoID))
		})
	}
}

func TestCIRefreshStampsCIAtChecksRequest(t *testing.T) {
	seededThenCI := func(ci, at string) mergeStatus {
		status := seededStatus
		status.CI, status.CIAt = ci, at
		return status
	}
	t.Run("GitHub", func(t *testing.T) {
		syncer, _, d, repoID := newObservedGitHubSyncer(t, observedPR(seededHead, ""))
		seedObservedMR(t, d, repoID, "changes_requested", false)

		warnings, err := syncer.RefreshMRCIStatusOnProvider(
			t.Context(), observedGitHubRepo, repoID, 1, seededHead,
		)
		require.NoError(t, err)
		assert.Empty(t, warnings)

		assert.Equal(t, seededThenCI("success", sentAt1000), readMergeStatus(t, d, repoID))
	})
	t.Run("GitHub head mismatch writes nothing", func(t *testing.T) {
		syncer, _, d, repoID := newObservedGitHubSyncer(t, observedPR(seededHead, ""))
		seedObservedMR(t, d, repoID, "changes_requested", false)

		_, err := syncer.RefreshMRCIStatusOnProvider(
			t.Context(), observedGitHubRepo, repoID, 1, "otherhead",
		)
		require.NoError(t, err)

		assert.Equal(t, seededStatus, readMergeStatus(t, d, repoID))
	})
	t.Run("GitLab", func(t *testing.T) {
		syncer, d, repoID := newObservedGitLabSyncer(t, observedGitLabMR(seededHead, "clean"))
		seedObservedMR(t, d, repoID, "", false)

		warnings, err := syncer.RefreshMRCIStatusOnProvider(
			t.Context(), observedGitLabRepo, repoID, 1, seededHead,
		)
		require.NoError(t, err)
		assert.Empty(t, warnings)

		assert.Equal(t, mergeStatus{
			CI: "success", CIAt: sentAt1000, Mergeable: "dirty", MergeableAt: seededAt,
		}, readMergeStatus(t, d, repoID))
	})
}
