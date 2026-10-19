package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
	"go.kenn.io/forge/platform/gitealike"
	platformgitlab "go.kenn.io/forge/platform/gitlab"
)

func TestSyncFailedRetainsUnreadRepositories(t *testing.T) {
	t.Run("PR list budget refused", func(t *testing.T) {
		repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
		client := &partialFailureMock{}
		s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
		var events [][2]string
		s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
		for _, err := range []error{platform.ErrPermissionDenied, platform.ErrSyncBudgetExhausted, platform.ErrPermissionDenied} {
			client.listOpenPRsErr = err
			s.RunOnce(t.Context())
			assert.Equal(t, [][2]string{{"merge_requests", "auth"}}, events)
		}
	})
	a := RepoRef{Owner: "acme", Name: "a"}
	b := RepoRef{Owner: "acme", Name: "b"}
	for _, tc := range []struct {
		name      string
		next      map[string]error
		tracked   []RepoRef
		wantNow   int
		wantLater int
	}{
		{"recovery overlaps new failure", map[string]error{"a": nil, "b": platform.ErrNotFound}, []RepoRef{a, b}, 1, 1},
		{"cadence skipped", map[string]error{"b": nil}, []RepoRef{a, b}, 1, 1},
		{"budget refused", map[string]error{"a": platform.ErrSyncBudgetExhausted, "b": nil}, []RepoRef{a, b}, 1, 1},
		{"disabled", map[string]error{"a": platform.ErrRepositoryFeatureDisabled}, []RepoRef{a, b}, 1, 2},
		{"untracked", map[string]error{"b": nil}, []RepoRef{b}, 1, 2},
		{"archived", map[string]error{"b": nil}, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, 1, 2},
		{"untracked as another fails", map[string]error{"b": platform.ErrNotFound}, []RepoRef{b}, 2, 2},
		{"archived as another fails", map[string]error{"b": platform.ErrNotFound}, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events [][2]string
			s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
			first := &syncPass{}
			first.observe(a, "issues", platform.ErrNotFound)
			s.finishSyncPass(first, []RepoRef{a, b})
			second := &syncPass{}
			for name, err := range tc.next {
				second.observe(RepoRef{Owner: "acme", Name: name}, "issues", err)
			}
			s.finishSyncPass(second, tc.tracked)
			require.Len(t, events, tc.wantNow)
			later := &syncPass{}
			later.observe(b, "issues", platform.ErrNotFound)
			s.finishSyncPass(later, tc.tracked)
			assert.Len(t, events, tc.wantLater)
		})
	}
}

func TestSyncFailedPreservesSiblingCauseUnderDisabledError(t *testing.T) {
	d := openTestDB(t)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	id, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
	require.NoError(t, err)
	now := time.Now().UTC()
	for _, number := range []int{1, 2} {
		_, err = d.UpsertIssue(t.Context(), &db.Issue{RepoID: id, PlatformID: int64(number), Number: number, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now})
		require.NoError(t, err)
	}
	client := &partialFailureMock{}
	calls := 0
	client.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
		calls++
		if calls == 1 {
			return nil, platform.ErrPermissionDenied
		}
		return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusGone}, Message: "Issues are disabled for this repo"}
	}
	s := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	require.Equal(t, 2, calls)
	assert.Equal(t, [][2]string{{"issues", "auth"}}, events)
}

func TestSyncFailedLookupDuringFeatureCooldown(t *testing.T) {
	for _, source := range []string{"issues", "merge_requests"} {
		t.Run(source, func(t *testing.T) {
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			other := RepoRef{Owner: "acme", Name: "other", PlatformHost: "github.com"}
			client := &partialFailureMock{}
			disabled := platform.RepositoryFeatureDisabled(platform.KindGitHub, "github.com", source, errors.New("feature disabled"))
			if source == "issues" {
				client.listOpenIssuesErr = disabled
			} else {
				client.listOpenPRsErr = disabled
			}
			s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			require.Empty(t, events)
			client.getRepositoryFn = func(context.Context, string, string) (*gh.Repository, error) {
				return nil, platform.ErrPermissionDenied
			}
			s.RunOnce(t.Context())
			otherSource := "merge_requests"
			if source == otherSource {
				otherSource = "issues"
			}
			require.Equal(t, [][2]string{{otherSource, "auth"}}, events)
			client.getRepositoryFn = nil
			s.RunOnce(t.Context())
			s.RunOnce(t.Context())
			if source == "issues" {
				client.listOpenIssuesErr = platform.ErrPermissionDenied
			} else {
				client.listOpenPRsErr = platform.ErrPermissionDenied
			}
			s.SetRepos(append(s.TrackedRepos(), other))
			s.RunOnce(t.Context())
			assert.Equal(t, [][2]string{{otherSource, "auth"}, {source, "auth"}}, events)
		})
	}
}

func TestSyncFailedResolvedIdentityFailureOverridesSuccess(t *testing.T) {
	for range 100 {
		repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
		var events [][2]string
		s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
		pass := &syncPass{}
		pass.observe(repo, "issues", platform.ErrPermissionDenied)
		repo.Key = platform.RepositoryIDKey(42)
		pass.observe(repo, "issues", nil)
		s.finishSyncPass(pass, []RepoRef{repo})
		require.Equal(t, [][2]string{{"issues", "auth"}}, events)
	}
}

func TestSyncFailedScopeCauses(t *testing.T) {
	d := openTestDB(t)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	id, err := reposeed.Seed(t.Context(), d, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: id, PlatformID: 1, Number: 1, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)
	client := &partialFailureMock{}
	client.listOpenIssuesErr = &gh.ErrorResponse{Response: &http.Response{StatusCode: 503}}
	client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
		return nil, platform.ErrPermissionDenied
	}
	s := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"merge_requests", "auth"}, {"issues", "provider"}}, events)
}

func TestSyncFailedPassEligibility(t *testing.T) {
	for _, scenario := range []string{"partial pass", "recovery", "cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			client := &partialFailureMock{}
			client.listOpenIssuesErr = platform.ErrNotFound
			s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			switch scenario {
			case "partial pass":
				s.runOnce(t.Context(), true, nil, []RepoRef{repo})
				require.Empty(t, events)
				s.runOnce(t.Context(), true, nil, nil)
				assert.Equal(t, [][2]string{{"issues", "not_found"}}, events)
			case "recovery":
				s.RunOnce(t.Context())
				s.RunOnce(t.Context())
				require.Equal(t, [][2]string{{"issues", "not_found"}}, events)
				client.listOpenIssuesErr = nil
				s.runOnce(t.Context(), true, nil, []RepoRef{repo})
				client.listOpenIssuesErr = platform.ErrNotFound
				s.RunOnce(t.Context())
				require.Len(t, events, 1)
				client.listOpenIssuesErr = nil
				s.runOnce(t.Context(), true, nil, nil)
				client.listOpenIssuesErr = platform.ErrNotFound
				s.RunOnce(t.Context())
				assert.Equal(t, [][2]string{{"issues", "not_found"}, {"issues", "not_found"}}, events)
			case "cancellation":
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				client.listOpenIssuesFn = func(context.Context, string, string) ([]*gh.Issue, error) {
					cancel()
					return nil, platform.ErrNotFound
				}
				s.RunOnce(ctx)
				require.Empty(t, events)
				client.listOpenIssuesFn = nil
				s.RunOnce(t.Context())
				assert.Equal(t, [][2]string{{"issues", "not_found"}}, events)
			}
		})
	}
}

func TestSyncFailedCredentialRoute(t *testing.T) {
	repo := RepoRef{Owner: "unrouted", Name: "widget", PlatformHost: "github.com"}
	client := &mockClient{}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	router, err := NewHostRouter("github.com", nil)
	require.NoError(t, err)
	s.SetGitHubRouters(map[string]*HostRouter{"github.com": router})
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
}

func TestSyncFailedRefsAndChecks(t *testing.T) {
	s, d, repoID := newObservedGitLabSyncer(t, observedGitLabMR(seededHead, "clean"), func(p *observingReadProvider) {
		p.checksErr = platform.ErrPermissionDenied
	})
	seedObservedMR(t, d, repoID, "")
	clones := gitclone.New(t.TempDir(), gitclone.HostSources{"gitlab.example.com": failingGitTokenSource{err: ErrMissingWriteIdentity}})
	t.Cleanup(clones.Wait)
	s.clones = clones
	bucket, err := s.bucketKeyForRepo(observedGitLabRepo, false)
	require.NoError(t, err)
	s.budgets = map[string]*SyncBudget{bucket: NewSyncBudget(1000)}
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"checks", "auth"}, {"refs", "auth"}}, events)
}

func TestSyncFailureReason(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{400, "other"},
		{401, "auth"},
		{403, "auth"},
		{404, "not_found"},
		{409, "other"},
		{422, "other"},
		{429, "rate_limited"},
		{503, "provider"},
	} {
		t.Run(fmt.Sprintf("gitlab %d", tc.status), func(t *testing.T) {
			client, err := platformgitlab.NewClient("gitlab.example.com", testTokenSource("test-token"), platformgitlab.WithoutRetriesForTesting(), platformgitlab.WithTransport(platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
			})))
			require.NoError(t, err)
			_, err = client.GetRepository(t.Context(), platform.RepoRef{Owner: "acme", Name: "widget", Key: platform.RepositoryIDKey(42)})
			require.Error(t, err)
			assert.Equal(t, tc.want, syncFailureReason(err))
		})
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/repos/acme/widget", nil)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"github primary limit", &gh.RateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden, Request: req}}, "rate_limited"},
		{"github secondary limit", &gh.AbuseRateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden, Request: req}}, "rate_limited"},
		{"github", &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusServiceUnavailable, Request: req}}, "provider"},
		{"forgejo and gitea", &gitealike.HTTPError{StatusCode: 404}, "not_found"},
		{"bitbucket", &bitbucket.UnexpectedResponseStatusError{StatusCode: 429}, "rate_limited"},
		{"network", &net.DNSError{Err: "unavailable"}, "network"},
		{"git DNS", errors.New("fatal: unable to access 'https://example.com/repo.git/': Could not resolve host: example.com"), "network"},
		{"git connection refused", errors.New("fatal: unable to access 'https://example.com/repo.git/': Failed to connect to example.com port 443: Connection refused"), "network"},
		{"deadline", context.DeadlineExceeded, "network"},
		{"contract", platform.ErrProviderContract, "provider"},
		{"rate limit", platform.ErrRateLimited, "rate_limited"},
		{"unknown", errors.New("private error details"), "other"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, syncFailureReason(fmt.Errorf("read: %w", tc.err))) })
	}
}

func TestSyncFailedBitbucketDCLookup(t *testing.T) {
	repo := RepoRef{Platform: platform.KindBitbucket, PlatformHost: "bitbucket.example.com", Owner: "PROJECT", Name: "widget", Key: platform.RepositoryIDKey(42)}
	client, err := bitbucketdc.NewClient(repo.PlatformHost, testTokenSource("test-token"), platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: fmt.Sprintf("%d %s", http.StatusServiceUnavailable, http.StatusText(http.StatusServiceUnavailable)), Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	}), nil)
	require.NoError(t, err)
	registry, err := platform.NewRegistry(client)
	require.NoError(t, err)
	s := NewSyncerWithRegistry(registry, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"merge_requests", "provider"}}, events)
}

func TestSyncFailedRenameRetainsOutage(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "before", Key: platform.RepositoryIDKey(42)}
	var events [][2]string
	s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
	for i := range 3 {
		pass := &syncPass{}
		if i == 1 {
			repo.Name = "after"
			pass.observe(repo, "issues", platform.ErrSyncBudgetExhausted)
		} else {
			pass.observe(repo, "issues", platform.ErrPermissionDenied)
		}
		s.finishSyncPass(pass, []RepoRef{repo})
	}
	assert.Equal(t, [][2]string{{"issues", "auth"}}, events)
}

func TestSyncFailedResolvedIdentityRetainsUnreadOutage(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	client := &partialFailureMock{}
	client.getRepositoryFn = func(context.Context, string, string) (*gh.Repository, error) {
		return nil, platform.ErrPermissionDenied
	}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	require.Equal(t, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
	client.getRepositoryFn = nil
	client.listOpenPRsErr = platform.ErrPermissionDenied
	s.RunOnce(t.Context())
	require.False(t, s.TrackedRepos()[0].Key.IsZero())
	client.listOpenPRsErr = nil
	client.listOpenIssuesErr = platform.ErrPermissionDenied
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
}

func TestSyncFailedBulkCIRecovery(t *testing.T) {
	d := openTestDB(t)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	id, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
	require.NoError(t, err)
	s := NewSyncer(map[string]Client{"github.com": &mockClient{}}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	for i := range 3 {
		pass := &syncPass{}
		ctx := context.WithValue(t.Context(), syncPassKey{}, pass)
		if i == 1 {
			require.NoError(t, s.syncOpenMRFromBulk(ctx, repo, id, time.Time{}, &BulkPR{
				PR: buildOpenPR(7, time.Now().UTC()), CIComplete: true,
			}, false))
		} else {
			pass.observe(repo, "checks", platform.ErrPermissionDenied)
		}
		s.finishSyncPass(pass, []RepoRef{repo})
	}
	assert.Equal(t, [][2]string{{"checks", "auth"}, {"checks", "auth"}}, events)
}

func TestSyncFailedFullPassTimelines(t *testing.T) {
	for _, source := range []string{"issues", "merge_requests"} {
		t.Run(source, func(t *testing.T) {
			d := openTestDB(t)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			now := time.Now().UTC()
			issue := buildOpenIssue(7, now)
			pr := buildOpenPR(7, now)
			client := &issueTimelineMockClient{}
			client.comments = []*gh.IssueComment{{ID: new(int64(123)), Body: new("retained comment"), User: &gh.User{Login: new("reviewer")}, CreatedAt: makeTimestamp(now)}}
			if source == "issues" {
				client.openIssues = []*gh.Issue{issue}
				client.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) { return issue, nil }
			} else {
				client.openPRs = []*gh.PullRequest{pr}
				client.singlePR = pr
			}
			s := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			for i, failing := range []bool{false, true, true, false, true} {
				client.issueTimelineErr = nil
				client.timelineEventsErr = nil
				if failing {
					failure := &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusServiceUnavailable}}
					client.issueTimelineErr = failure
					client.timelineEventsErr = failure
				}
				updated := now.Add(time.Duration(i) * time.Minute)
				issue.UpdatedAt = makeTimestamp(updated)
				pr.UpdatedAt = makeTimestamp(updated)
				s.RunOnce(t.Context())
				want := [][2]string(nil)
				if i > 0 {
					want = append(want, [2]string{source, "provider"})
				}
				if i == 4 {
					want = append(want, [2]string{source, "provider"})
				}
				assert.Equal(t, want, events)
				if source == "issues" {
					stored, err := d.GetIssue(t.Context(), "github", "github.com", repo.Owner, repo.Name, 7)
					require.NoError(t, err)
					require.NotNil(t, stored)
					storedEvents, err := d.ListIssueEvents(t.Context(), stored.ID)
					require.NoError(t, err)
					require.NotEmpty(t, storedEvents)
					assert.Equal(t, "retained comment", storedEvents[0].Body)
				} else {
					stored, err := d.GetMergeRequest(t.Context(), "github", "github.com", repo.Owner, repo.Name, 7)
					require.NoError(t, err)
					require.NotNil(t, stored)
					storedEvents, err := d.ListMREvents(t.Context(), stored.ID)
					require.NoError(t, err)
					require.NotEmpty(t, storedEvents)
					assert.Equal(t, "retained comment", storedEvents[0].Body)
				}
			}
		})
	}
}

func TestSyncFailedFullPassSuppressedReads(t *testing.T) {
	for _, tc := range []struct{ kind, source, reason string }{
		{"PR visibility", "merge_requests", "other"},
		{"issue visibility", "issues", "other"},
		{"workflow approval", "checks", "provider"},
		{"merge queue", "merge_requests", "other"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			assert := assert.New(t)
			d := openTestDB(t)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com", Key: platform.RepositoryIDKey(testRepoID("acme", "widget"))}
			repoID, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
			require.NoError(t, err)
			now := time.Now().UTC()
			pr := buildOpenPR(7, now)
			issue := buildOpenIssue(7, now)
			client := &issueTimelineMockClient{}
			client.singlePR = pr
			client.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) { return issue, nil }
			client.comments = []*gh.IssueComment{{ID: new(int64(123)), Body: new("retained comment"), User: &gh.User{Login: new("reviewer")}, CreatedAt: makeTimestamp(now)}}
			if tc.source == "issues" {
				_, err = d.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, PlatformID: 7000, Number: 7, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now})
			} else {
				_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 7000, Number: 7, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now, HeadBranch: "feature-branch", BaseBranch: "main", PlatformHeadSHA: pr.GetHead().GetSHA()})
			}
			require.NoError(t, err)
			var failing atomic.Bool
			var calls atomic.Int32
			client.listWorkflowRunsFn = func(_ context.Context, owner, name, sha string) ([]*gh.WorkflowRun, error) {
				if tc.kind == "workflow approval" {
					calls.Add(1)
					assert.Equal("acme", owner)
					assert.Equal("widget", name)
					assert.Equal(pr.GetHead().GetSHA(), sha)
					if failing.Load() {
						return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusServiceUnavailable}}
					}
				}
				return []*gh.WorkflowRun{{ID: new(int64(1)), HeadSHA: new(pr.GetHead().GetSHA()), PullRequests: []*gh.PullRequest{{Number: new(7)}}}}, nil
			}
			queue := newPRQueueGraphQL(queuedPR7)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, readErr := io.ReadAll(r.Body)
				if !assert.NoError(readErr) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				query := string(body)
				for _, collection := range []string{"pullRequests", "issues"} {
					if strings.Contains(query, collection+"(") {
						_, _ = fmt.Fprintf(w, `{"data":{"repository":{"%s":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`, collection)
						return
					}
				}
				visibility := strings.Contains(query, "isMinimized") && (strings.Contains(query, "pullRequest(number:") || strings.Contains(query, "issue(number:"))
				queueRead := strings.Contains(query, "isMergeQueueEnabled")
				if visibility && strings.Contains(tc.kind, "visibility") || queueRead && tc.kind == "merge queue" {
					calls.Add(1)
					if failing.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				}
				if visibility {
					item := "pullRequest"
					if tc.source == "issues" {
						item = "issue"
					}
					_, _ = fmt.Fprintf(w, `{"data":{"repository":{"%s":{"comments":{"nodes":[{"databaseId":123,"isMinimized":true,"minimizedReason":"ABUSE"}],"pageInfo":{"hasNextPage":false}}}}}}`, item)
					return
				}
				r.Body = io.NopCloser(strings.NewReader(query))
				queue.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			s := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
			s.SetFetchers(map[string]*GraphQLFetcher{"github.com": NewGraphQLFetcherWithClient(githubv4.NewEnterpriseClient(server.URL, server.Client()), nil)})
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			for i, fail := range []bool{false, true, true, false, true} {
				failing.Store(fail)
				before := calls.Load()
				s.RunOnce(t.Context())
				require.Greater(t, calls.Load(), before, "pass %d must attempt the source read", i)
				want := [][2]string(nil)
				if i > 0 {
					want = append(want, [2]string{tc.source, tc.reason})
				}
				if i == 4 {
					want = append(want, [2]string{tc.source, tc.reason})
				}
				assert.Equal(want, events, "pass %d", i)
				var commentsMetadata string
				if tc.source == "issues" {
					stored, err := d.GetIssue(t.Context(), "github", "github.com", repo.Owner, repo.Name, 7)
					require.NoError(t, err)
					require.NotNil(t, stored)
					comments, err := d.ListIssueEvents(t.Context(), stored.ID)
					require.NoError(t, err)
					require.Len(t, comments, 1)
					commentsMetadata = comments[0].MetadataJSON
					_, err = d.ClearIssueDetailFetchedSnapshot(t.Context(), stored.ID, stored.SnapshotRevision)
					require.NoError(t, err)
				} else {
					stored, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
					require.NoError(t, err)
					require.NotNil(t, stored)
					comments, err := d.ListMREvents(t.Context(), stored.ID)
					require.NoError(t, err)
					require.Len(t, comments, 1)
					commentsMetadata = comments[0].MetadataJSON
					if tc.kind == "workflow approval" {
						assert.True(stored.WorkflowApprovalRequired)
						assert.Equal(1, stored.WorkflowApprovalCount)
					}
					if tc.kind == "merge queue" {
						state, err := d.GetMergeQueueState(t.Context(), repoID, 7)
						require.NoError(t, err)
						require.NotNil(t, state)
						assert.True(state.Queued)
						assert.Equal(1, state.Position)
					}
					_, err = d.ClearMRDetailFetchedSnapshot(t.Context(), stored.ID, stored.SnapshotRevision)
					require.NoError(t, err)
				}
				assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"ABUSE"}`, commentsMetadata)
			}
		})
	}
}

func TestSyncFailedFullPassDetailAndComments(t *testing.T) {
	for _, scenario := range []string{"provider failure", "disabled healthy", "disabled failing"} {
		t.Run(scenario, func(t *testing.T) {
			for _, kind := range []string{"issue detail", "PR detail", "issue comments", "PR comments"} {
				t.Run(kind, func(t *testing.T) {
					d := openTestDB(t)
					repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
					repo.Key = platform.RepositoryIDKey(testRepoID(repo.Owner, repo.Name))
					id, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
					require.NoError(t, err)
					now := time.Now().UTC()
					var fetched *time.Time
					if kind == "issue comments" || kind == "PR comments" {
						fetched = &now
					}
					client := &partialFailureMock{issuesCached: true, prsCached: true}
					failure := &gh.ErrorResponse{Response: &http.Response{StatusCode: 503}}
					if scenario != "provider failure" {
						failure = &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusGone}, Message: "Issues are disabled for this repo"}
					}
					called := false
					source := "issues"
					if kind == "PR detail" || kind == "PR comments" {
						source = "merge_requests"
						if scenario != "provider failure" {
							failure.Message = "Pull Requests are disabled for this repo"
						}
						_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: id, PlatformID: 7, Number: 7, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now, DetailFetchedAt: fetched, CommentCount: 100, MergeableState: "clean"})
					} else {
						_, err = d.UpsertIssue(t.Context(), &db.Issue{RepoID: id, PlatformID: 7, Number: 7, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now, DetailFetchedAt: fetched, CommentCount: 100})
					}
					require.NoError(t, err)
					switch kind {
					case "issue detail":
						client.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) { called = true; return nil, failure }
					case "PR detail":
						client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
							called = true
							return nil, failure
						}
					default:
						client.listIssueCommentsFn = func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
							called = true
							return nil, failure
						}
					}
					s := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
					var events [][2]string
					s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
					if scenario == "disabled failing" {
						prior := &syncPass{}
						prior.observe(repo, source, platform.ErrPermissionDenied)
						s.finishSyncPass(prior, []RepoRef{repo})
						// Exercise refresh observations without a healthy index read recovering the source.
						pass := &syncPass{}
						ctx := context.WithValue(t.Context(), syncPassKey{}, pass)
						if fetched == nil {
							s.drainDetailQueue(ctx, map[string]bool{"github.com": true}, []RepoRef{repo})
						} else {
							if kind == "PR comments" {
								s.queuePRCommentSync(repo, id, 7, fetched)
							} else {
								s.queueIssueCommentSync(repo, id, 7, fetched)
							}
							s.drainPendingCommentSyncs(ctx, map[string]bool{"github.com": true})
						}
						s.finishSyncPass(pass, []RepoRef{repo})
					} else {
						s.RunOnce(t.Context())
					}
					require.True(t, called, "the refresh must reach the failing read")
					if scenario == "provider failure" {
						assert.Equal(t, [][2]string{{source, "provider"}}, events)
						return
					}
					assert.Empty(t, s.syncFailures[source])
					if scenario == "disabled failing" {
						assert.Equal(t, [][2]string{{source, "auth"}}, events)
					} else {
						assert.Empty(t, events)
					}
				})
			}
		})
	}
}

func TestSyncFailedGitCloneErrors(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		status     int
	}{
		{"server", "provider", http.StatusServiceUnavailable},
		{"network", "network", 0},
		{"auth", "auth", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				conn, _, err := http.NewResponseController(w).Hijack()
				if err != nil {
					return
				}
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.SetLinger(0)
				}
				_ = conn.Close()
			}))
			t.Cleanup(server.Close)
			host := strings.TrimPrefix(server.URL, "http://")
			repo := RepoRef{Platform: platform.KindGitLab, PlatformHost: host, Owner: "acme", Name: "widget", Key: platform.RepositoryIDKey(42), CloneURL: server.URL + "/acme/widget.git"}
			provider := &syncTestRepositoryReadProvider{syncTestReadProvider: &syncTestReadProvider{kind: platform.KindGitLab, host: host}, repository: platform.Repository{Ref: platformRepoRef(repo), CloneURL: repo.CloneURL}}
			registry, err := platform.NewRegistry(provider)
			require.NoError(t, err)
			clones := gitclone.New(t.TempDir(), nil)
			clones.SetAllowInsecureHTTP("gitlab", host, true)
			t.Cleanup(clones.Wait)
			s := NewSyncerWithRegistry(registry, openTestDB(t), clones, []RepoRef{repo}, time.Minute, nil, nil)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			require.Positive(t, calls.Load())
			assert.Equal(t, [][2]string{{"refs", tc.want}}, events)
		})
	}
}

func TestSyncFailedBitbucketForkHead(t *testing.T) {
	root := t.TempDir()
	remote := setupBareRemoteForSyncTest(t)
	work := filepath.Join(root, "work")
	syncActivityGitRun(t, root, "clone", remote, work)
	syncActivityGitRun(t, work, "config", "user.email", "test@example.com")
	syncActivityGitRun(t, work, "config", "user.name", "Test")
	syncActivityGitRun(t, work, "commit", "--allow-empty", "-m", "base")
	base := syncActivityGitRun(t, work, "rev-parse", "HEAD")
	syncActivityGitRun(t, work, "push", "origin", "HEAD:main")
	fork := filepath.Join(root, "contributor", "widget.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(fork), 0o755))
	syncActivityGitRun(t, root, "clone", "--bare", remote, fork)
	syncActivityGitRun(t, work, "commit", "--allow-empty", "-m", "fork head")
	head := syncActivityGitRun(t, work, "rev-parse", "HEAD")
	syncActivityGitRun(t, work, "push", fork, "HEAD:feature")
	syncActivityGitRun(t, fork, "update-server-info")
	var failing atomic.Bool
	failing.Store(true)
	var calls atomic.Int32
	files := http.FileServer(http.Dir(root))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if failing.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	repo := RepoRef{Platform: platform.KindBitbucket, PlatformHost: host, Owner: "PROJECT", Name: "widget", Key: platform.RepositoryIDKey(42), CloneURL: remote, DefaultBranch: "main"}
	mr := observedGitLabMR(head, "clean")
	mr.Repo = platformRepoRef(repo)
	mr.BaseSHA = base
	mr.HeadBranch = "feature"
	mr.HeadRepoCloneURL = server.URL + "/contributor/widget.git"
	provider := &syncTestRepositoryReadProvider{syncTestReadProvider: &syncTestReadProvider{kind: platform.KindBitbucket, host: host, mergeRequests: []platform.MergeRequest{mr}}, repository: platform.Repository{Ref: platformRepoRef(repo), CloneURL: remote, DefaultBranch: "main"}}
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	clones := gitclone.New(t.TempDir(), nil)
	clones.SetAllowInsecureHTTP("bitbucket", host, true)
	t.Cleanup(clones.Wait)
	d := openTestDB(t)
	s := NewSyncerWithRegistry(registry, d, clones, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	for range 2 {
		s.RunOnce(t.Context())
		require.Positive(t, calls.Load())
		require.Equal(t, [][2]string{{"refs", "auth"}}, events)
	}
	failing.Store(false)
	s.RunOnce(t.Context())
	stored, err := d.GetMergeRequest(t.Context(), "bitbucket", host, repo.Owner, repo.Name, 1)
	require.NoError(t, err)
	require.Equal(t, head, stored.DiffHeadSHA)
	syncActivityGitRun(t, work, "commit", "--allow-empty", "-m", "next fork head")
	syncActivityGitRun(t, work, "push", fork, "HEAD:feature")
	syncActivityGitRun(t, fork, "update-server-info")
	provider.mergeRequests[0].HeadSHA = syncActivityGitRun(t, work, "rev-parse", "HEAD")
	provider.mergeRequests[0].UpdatedAt = time.Now().UTC()
	failing.Store(true)
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"refs", "auth"}, {"refs", "auth"}}, events)
}
