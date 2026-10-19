package github

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
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
	resolved := a
	resolved.Key = platform.RepositoryIDKey(42)
	renamed := resolved
	renamed.Name = "renamed"
	type observation struct {
		repo RepoRef
		err  error
	}
	for _, tc := range []struct {
		name      string
		first     RepoRef
		next      []observation
		tracked   []RepoRef
		later     RepoRef
		wantNow   int
		wantLater int
	}{
		{"recovery overlaps new failure", a, []observation{{a, nil}, {b, platform.ErrNotFound}}, []RepoRef{a, b}, b, 2, 2},
		{"persistent failure overlaps new failure", a, []observation{{a, platform.ErrNotFound}, {b, platform.ErrNotFound}}, []RepoRef{a, b}, b, 2, 2},
		{"cadence skipped", a, []observation{{b, nil}}, []RepoRef{a, b}, b, 1, 2},
		{"budget refused", a, []observation{{a, platform.ErrSyncBudgetExhausted}, {b, nil}}, []RepoRef{a, b}, b, 1, 2},
		{"disabled", a, []observation{{a, platform.ErrRepositoryFeatureDisabled}}, []RepoRef{a, b}, b, 1, 2},
		{"untracked", a, []observation{{b, nil}}, []RepoRef{b}, b, 1, 2},
		{"archived", a, []observation{{b, nil}}, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, b, 1, 2},
		{"untracked as another fails", a, []observation{{b, platform.ErrNotFound}}, []RepoRef{b}, b, 2, 2},
		{"archived as another fails", a, []observation{{b, platform.ErrNotFound}}, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, b, 2, 2},
		{"rename keeps identity", resolved, []observation{{renamed, platform.ErrSyncBudgetExhausted}}, []RepoRef{renamed}, renamed, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events [][2]string
			s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
			first := &syncPass{}
			first.observe(tc.first, "issues", platform.ErrNotFound)
			s.finishSyncPass(first, []RepoRef{tc.first, b})
			second := &syncPass{}
			for _, observation := range tc.next {
				second.observe(observation.repo, "issues", observation.err)
			}
			s.finishSyncPass(second, tc.tracked)
			require.Len(t, events, tc.wantNow)
			later := &syncPass{}
			later.observe(tc.later, "issues", platform.ErrNotFound)
			s.finishSyncPass(later, tc.tracked)
			assert.Len(t, events, tc.wantLater)
		})
	}
}

func TestSyncFailedBitbucketDCServerError(t *testing.T) {
	client, err := bitbucketdc.NewClient("bitbucket.example.com", testTokenSource("test-token"), platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "/rest/api/latest/projects/PROJECT/repos/widget", req.URL.Path)
		return &http.Response{StatusCode: 503, Status: "503 Service Unavailable", Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	}), nil)
	require.NoError(t, err)
	_, failure := client.GetRepository(t.Context(), platform.RepoRef{Platform: platform.KindBitbucket, Host: client.Host(), Owner: "PROJECT", Name: "widget"})
	require.Error(t, failure)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	s := NewSyncer(map[string]Client{"github.com": &partialFailureMock{listOpenPRsErr: failure}}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"merge_requests", "provider"}}, events)
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

func TestSyncFailedDetailPersistenceIsLocal(t *testing.T) {
	d := openTestDB(t)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	id, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = d.UpsertIssue(t.Context(), &db.Issue{RepoID: id, PlatformID: 7, Number: 7, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)
	_, err = d.WriteDB().ExecContext(t.Context(), `CREATE TRIGGER reject_detail BEFORE UPDATE OF detail_fetched_at ON forge_issues WHEN NEW.detail_fetched_at IS NOT NULL BEGIN SELECT RAISE(ABORT, 'reject detail marker'); END`)
	require.NoError(t, err)
	client := &partialFailureMock{issuesCached: true, prsCached: true}
	called := false
	client.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
		called = true
		return &gh.Issue{ID: new(int64(7)), Number: new(7), State: new("open"), Title: new("Updated detail"), CreatedAt: &gh.Timestamp{Time: now}, UpdatedAt: &gh.Timestamp{Time: now}}, nil
	}
	s := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	require.True(t, called)
	issue, err := d.GetIssueByRepoIDAndNumber(t.Context(), id, 7)
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, "Updated detail", issue.Title)
	assert.Nil(t, issue.DetailFetchedAt)
	assert.Empty(t, events)
}

func TestSyncFailedPassEligibility(t *testing.T) {
	for _, scenario := range []string{"partial pass", "cancellation"} {
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
				client.listOpenIssuesErr = nil
				s.runOnce(t.Context(), true, nil, []RepoRef{repo})
				client.listOpenIssuesErr = platform.ErrNotFound
				s.RunOnce(t.Context())
				assert.Len(t, events, 1)
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
	bucket, err := s.bucketKeyForRepo(observedGitLabRepo, false)
	require.NoError(t, err)
	s.budgets = map[string]*SyncBudget{bucket: NewSyncBudget(1000)}
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"checks", "auth"}}, events)
}

func TestSyncFailureReason(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{400, "other"},
		{401, "auth"},
		{404, "not_found"},
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
		{"deadline", context.DeadlineExceeded, "network"},
		{"contract", platform.ErrProviderContract, "provider"},
		{"rate limit", platform.ErrRateLimited, "rate_limited"},
		{"unknown", errors.New("private error details"), "other"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, syncFailureReason(fmt.Errorf("read: %w", tc.err))) })
	}
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

func TestSyncFailedFullPassDetailAndComments(t *testing.T) {
	for _, scenario := range []string{"provider failure", "disabled failing"} {
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
					assert.Equal(t, [][2]string{{source, "auth"}}, events)
				})
			}
		})
	}
}

func TestSyncFailedRepositoryLookup(t *testing.T) {
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
}

func TestSyncFailedGitCloneErrors(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		status     int
	}{
		{"server", "other", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
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
