package github

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
	"go.kenn.io/forge/platform/gitealike"
)

func TestSyncFailedRetainsUnreadRepositories(t *testing.T) {
	a := RepoRef{Owner: "acme", Name: "a"}
	b := RepoRef{Owner: "acme", Name: "b"}
	for _, tc := range []struct {
		name      string
		next      map[string]error
		tracked   []RepoRef
		wantLater int
	}{
		{"recovery overlaps new failure", map[string]error{"a": nil, "b": platform.ErrNotFound}, []RepoRef{a, b}, 1},
		{"cadence skipped", map[string]error{"b": nil}, []RepoRef{a, b}, 1},
		{"budget refused", map[string]error{"a": platform.ErrSyncBudgetExhausted, "b": nil}, []RepoRef{a, b}, 1},
		{"disabled", map[string]error{"a": platform.ErrRepositoryFeatureDisabled}, []RepoRef{a, b}, 1},
		{"sync disabled", map[string]error{"a": platform.ErrSyncDisabled}, []RepoRef{a, b}, 1},
		{"untracked", map[string]error{"b": nil}, []RepoRef{b}, 2},
		{"archived", map[string]error{"b": nil}, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, 2},
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
			require.Len(t, events, 1)
			later := &syncPass{}
			later.observe(b, "issues", platform.ErrNotFound)
			s.finishSyncPass(later, tc.tracked)
			assert.Len(t, events, tc.wantLater)
			if disabled := tc.next["a"]; errors.Is(disabled, platform.ErrRepositoryFeatureDisabled) || errors.Is(disabled, platform.ErrSyncDisabled) {
				recovered := &syncPass{}
				recovered.observe(a, "issues", nil)
				recovered.observe(b, "issues", nil)
				s.finishSyncPass(recovered, tc.tracked)
				pass := &syncPass{}
				pass.observe(a, "issues", preservePartialSyncFailure(failIssues, disabled, platform.ErrPermissionDenied))
				s.finishSyncPass(pass, tc.tracked)
				assert.Equal(t, [][2]string{{"issues", "not_found"}, {"issues", "auth"}}, events)
			}
		})
	}
}

func TestSyncFailedScopeCauses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		issuesErr error
		want      [][2]string
	}{
		{"separate reasons", &gh.ErrorResponse{Response: &http.Response{StatusCode: 503}}, [][2]string{{"merge_requests", "auth"}, {"issues", "provider"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			id, err := reposeed.Seed(t.Context(), d, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
			require.NoError(t, err)
			now := time.Now().UTC()
			_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: id, PlatformID: 1, Number: 1, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now})
			require.NoError(t, err)
			client := &partialFailureMock{}
			client.listOpenIssuesErr = tc.issuesErr
			client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
				return nil, platform.ErrPermissionDenied
			}
			s := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			assert.Equal(t, tc.want, events)
			// A pull-list failure leaves issues unread, preserving its outage.
			client.listOpenPRsErr = platform.ErrPermissionDenied
			s.RunOnce(t.Context())
			assert.Equal(t, tc.want, events)
		})
	}
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
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	clones := gitclone.New(t.TempDir(), gitclone.HostSources{"github.com": failingGitTokenSource{err: ErrMissingWriteIdentity}})
	t.Cleanup(clones.Wait)
	client := &mockClient{listCheckRunsForRefFn: func(_ context.Context, _, _, sha string) ([]*gh.CheckRun, error) {
		if sha == "b" {
			return nil, platform.ErrPermissionDenied
		}
		return nil, nil
	}}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), clones, []RepoRef{repo}, time.Minute, nil, nil)
	pass := &syncPass{}
	ctx := context.WithValue(t.Context(), syncPassKey{}, pass)
	require.ErrorIs(t, s.ensureClone(ctx, repo), ErrMissingWriteIdentity)
	for _, sha := range []string{"a", "b", "c"} {
		_, err := s.fetchGitHubCIStatus(ctx, repo, 1, sha)
		require.NoError(t, err)
	}
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.finishSyncPass(pass, []RepoRef{repo})
	assert.Equal(t, [][2]string{{"checks", "auth"}, {"refs", "auth"}}, events)
}

func TestSyncFailureReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"github primary limit", &gh.RateLimitError{}, "rate_limited"},
		{"github secondary limit", &gh.AbuseRateLimitError{}, "rate_limited"},
		{"github", &gh.ErrorResponse{Response: &http.Response{StatusCode: 503}}, "provider"},
		{"gitlab", &gitlab.ErrorResponse{StatusCode: 401}, "auth"},
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
					var want [][2]string
					if scenario == "disabled failing" {
						want = [][2]string{{source, "auth"}}
						assert.Equal(t, map[string]string{mergeQueueRepoKey(repo): "auth"}, s.syncFailures[source])
					} else {
						assert.Empty(t, s.syncFailures[source])
					}
					assert.Equal(t, want, events)
					// Disabling one repository must leave genuine failures in another observable.
					other := RepoRef{Owner: "acme", Name: "other", PlatformHost: "github.com"}
					later := &syncPass{}
					later.observe(other, source, platform.ErrNotFound)
					s.finishSyncPass(later, []RepoRef{repo, other})
					if scenario == "disabled healthy" {
						want = [][2]string{{source, "not_found"}}
					}
					assert.Equal(t, want, events)
				})
			}
		})
	}
}
