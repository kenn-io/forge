package github

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

func TestSyncFailedPass(t *testing.T) {
	a := RepoRef{Owner: "acme", Name: "a"}
	b := RepoRef{Owner: "acme", Name: "b"}
	initialFailure := [][2]string{{"issues", "not_found"}}
	additionalFailure := [][2]string{{"issues", "not_found"}, {"issues", "not_found"}}
	type observation struct {
		repo   RepoRef
		source string
		err    error
	}
	type step struct {
		reads []observation
		want  [][2]string
	}
	type scenario struct {
		name  string
		steps []step
	}
	var cases []scenario
	for _, tc := range []struct {
		name                  string
		next                  []observation
		later                 RepoRef
		wantAfterNextPass     [][2]string
		wantAfterLaterFailure [][2]string
	}{
		{"another repository starts failing", []observation{{a, "issues", platform.ErrNotFound}, {b, "issues", platform.ErrNotFound}}, b, additionalFailure, additionalFailure},
		{"no reads", nil, a, initialFailure, initialFailure},
		{"not modified", []observation{{a, "issues", &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotModified}}}}, a, initialFailure, additionalFailure},
	} {
		cases = append(cases, scenario{tc.name, []step{
			{[]observation{{a, "issues", platform.ErrNotFound}}, initialFailure},
			{tc.next, tc.wantAfterNextPass},
			{[]observation{{tc.later, "issues", platform.ErrNotFound}}, tc.wantAfterLaterFailure},
		}})
	}
	cases = append(cases, scenario{"sources recover independently", []step{
		{[]observation{{a, "issues", platform.ErrPermissionDenied}, {a, "checks", platform.ErrPermissionDenied}}, [][2]string{{"issues", "auth"}, {"checks", "auth"}}},
		{[]observation{{a, "issues", platform.ErrPermissionDenied}, {a, "checks", nil}}, [][2]string{{"issues", "auth"}, {"checks", "auth"}}},
		{[]observation{{a, "issues", platform.ErrPermissionDenied}, {a, "checks", platform.ErrPermissionDenied}}, [][2]string{{"issues", "auth"}, {"checks", "auth"}, {"checks", "auth"}}},
	}})
	for _, acrossRepos := range []bool{false, true} {
		for _, authFirst := range []bool{false, true} {
			var first, second error = errors.New("unknown failure"), platform.ErrPermissionDenied
			if authFirst {
				first, second = second, first
			}
			other := a
			if acrossRepos {
				other = b
			}
			cases = append(cases, scenario{fmt.Sprintf("reason priority/acrossRepos=%t/authFirst=%t", acrossRepos, authFirst), []step{
				{[]observation{{a, "issues", first}, {other, "issues", second}}, [][2]string{{"issues", "auth"}}},
			}})
		}
	}
	for _, failureFirst := range []bool{false, true} {
		var first, second error = nil, platform.ErrPermissionDenied
		if failureFirst {
			first, second = second, first
		}
		cases = append(cases, scenario{fmt.Sprintf("mixed reads/failureFirst=%t", failureFirst), []step{
			{[]observation{{a, "issues", first}, {a, "issues", second}}, nil},
		}})
	}
	for _, tc := range []struct {
		name   string
		source string
		err    error
	}{
		{"local path error", "issues", fmt.Errorf("read: %w", &fs.PathError{Op: "open", Path: "token", Err: fs.ErrNotExist})},
		{"local access denied", "issues", fmt.Errorf("read: %w", syscall.EACCES)},
		{"local operation denied", "issues", fmt.Errorf("read: %w", syscall.EPERM)},
		{"local full disk", "issues", fmt.Errorf("write: %w", syscall.ENOSPC)},
		{"local read-only disk", "issues", fmt.Errorf("write: %w", syscall.EROFS)},
		{"local process file limit", "issues", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EMFILE}},
		{"local system file limit", "issues", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENFILE}},
		{"budget refused", "issues", platform.ErrSyncBudgetExhausted},
		{"provider not configured", "merge_requests", fmt.Errorf("resolve reader: %w", platform.ErrProviderNotConfigured)},
		{"invalid repository reference", "issues", fmt.Errorf("resolve reader: %w", platform.ErrInvalidRepoRef)},
		{"disabled feature", "issues", platform.RepositoryFeatureDisabled(platform.KindGitHub, "github.com", "issues", nil)},
	} {
		cases = append(cases, scenario{tc.name, []step{
			{[]observation{{a, tc.source, tc.err}}, nil},
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events [][2]string
			s := NewSyncer(nil, openTestDB(t), nil, nil, time.Minute, nil, nil)
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			for i, step := range tc.steps {
				pass := &syncPass{}
				for _, read := range step.reads {
					pass.observe(read.repo, read.source, read.err)
				}
				s.finishSyncPass(pass)
				require.Equal(t, step.want, events, "pass %d", i+1)
			}
		})
	}
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

func TestSyncFailedProviderChecks(t *testing.T) {
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

func TestSyncFailedGitHubChecksNotFound(t *testing.T) {
	for _, endpoint := range []string{"check runs", "combined status"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				statuses []int
				want     [][2]string
			}{
				{"lone 404", []int{http.StatusNotFound}, [][2]string{{"checks", "not_found"}}},
				{"success after 404", []int{http.StatusNotFound, http.StatusOK}, nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
					client := &mockClient{}
					var events [][2]string
					s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
					s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
					pass := &syncPass{}
					ctx := context.WithValue(t.Context(), syncPassKey{}, pass)
					for i, status := range tc.statuses {
						var failure error
						if status != http.StatusOK {
							failure = &gh.ErrorResponse{Response: &http.Response{StatusCode: status}}
						}
						if endpoint == "check runs" {
							client.checkRunsErr = failure
						} else {
							client.ciStatusErr = failure
						}
						_, err := s.fetchGitHubCIStatus(ctx, repo, 7+i, fmt.Sprintf("head-%d", i))
						require.NoError(t, err)
					}
					s.finishSyncPass(pass)
					assert.Equal(t, tc.want, events)
				})
			}
		})
	}
}

func TestSyncFailureReason(t *testing.T) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/repos/acme/widget", nil)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"github primary limit", &gh.RateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden, Request: req}}, "rate_limited"},
		{"github secondary limit", &gh.AbuseRateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden, Request: req}}, "rate_limited"},
		{"github unauthorized", &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnauthorized, Request: req}}, "auth"},
		{"github forbidden", &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusForbidden, Request: req}}, "auth"},
		{"github rate limit status", &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusTooManyRequests, Request: req}}, "rate_limited"},
		{"not found", platform.ErrNotFound, "not_found"},
		{"authentication", platform.ErrPermissionDenied, "auth"},
		{"empty managed token", tokenauth.ErrMissingToken, "auth"},
		{"rate limit", platform.ErrRateLimited, "rate_limited"},
		{"unknown", errors.New("private error details"), "other"},
		{"app unauthorized", &githubapp.StatusError{StatusCode: http.StatusUnauthorized}, "auth"},
		{"app forbidden", &githubapp.StatusError{StatusCode: http.StatusForbidden}, "auth"},
		{"app not found", &githubapp.StatusError{StatusCode: http.StatusNotFound}, "not_found"},
		{"app too many requests", &githubapp.StatusError{StatusCode: http.StatusTooManyRequests}, "rate_limited"},
		{"app retry header alone", &githubapp.StatusError{StatusCode: http.StatusForbidden, Header: http.Header{"Retry-After": {"90"}}}, "auth"},
		{"app primary limit", &githubapp.StatusError{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"0"}}}, "rate_limited"},
		{"app remaining quota", &githubapp.StatusError{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"4999"}}}, "auth"},
		{"app secondary limit with retry header", &githubapp.StatusError{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"4999"}, "Retry-After": {"90"}}, Body: `{"message":"You have exceeded a secondary rate limit.","documentation_url":"https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`}, "rate_limited"},
		{"app secondary limit without retry header", &githubapp.StatusError{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"4999"}}, Body: `{"message":"You have exceeded a secondary rate limit.","documentation_url":"https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`}, "rate_limited"},
		{"app unknown", &githubapp.StatusError{StatusCode: http.StatusServiceUnavailable}, "other"},
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
	pass := &syncPass{}
	pass.observe(repo, "checks", platform.ErrPermissionDenied)
	ctx := context.WithValue(t.Context(), syncPassKey{}, pass)
	require.NoError(t, s.syncOpenMRFromBulk(ctx, repo, id, snapshotRevisions(t, s.db, id, db.ArchiveItemTypeMergeRequest), time.Time{}, &BulkPR{
		PR: buildOpenPR(7, time.Now().UTC()), CIComplete: true,
	}, false))
	s.finishSyncPass(pass)
	assert.Empty(t, events)
}

func TestSyncFailedRepositoryFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T) *Syncer
		reason string
	}{
		{"missing repository ID", func(t *testing.T) *Syncer {
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			d := openTestDB(t)
			_, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
			require.NoError(t, err)
			client := &partialFailureMock{}
			client.getRepositoryFn = func(context.Context, string, string) (*gh.Repository, error) {
				return &gh.Repository{Name: new("widget")}, nil
			}
			return NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
		}, "other"},
		{"keyless credential route", func(t *testing.T) *Syncer {
			d := openTestDB(t)
			repo := RepoRef{Owner: "unrouted", Name: "widget", PlatformHost: "github.com"}
			_, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
			require.NoError(t, err)
			s := NewSyncer(map[string]Client{"github.com": &mockClient{}}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
			router, err := NewHostRouter("github.com", nil)
			require.NoError(t, err)
			s.SetGitHubRouters(map[string]*HostRouter{"github.com": router})
			return s
		}, "auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.setup(t)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			assert.Equal(t, [][2]string{{"merge_requests", tc.reason}, {"issues", tc.reason}}, events)
		})
	}
}

func TestSyncFailedAfterResolvedRecoveryAndReadd(t *testing.T) {
	assert := assert.New(t)
	raw := config.Repo{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	client := &partialFailureMock{}
	outage := true
	client.getRepositoryFn = func(_ context.Context, owner, name string) (*gh.Repository, error) {
		if outage {
			return nil, platform.ErrPermissionDenied
		}
		return &gh.Repository{ID: new(int64(42)), Name: &name, Owner: &gh.User{Login: &owner}}, nil
	}
	var issueReads atomic.Int32
	client.listOpenIssuesFn = func(context.Context, string, string) ([]*gh.Issue, error) {
		issueReads.Add(1)
		return nil, nil
	}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, FallbackConfiguredRepoRefs(nil, raw), time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	require.Equal(t, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
	assert.Zero(issueReads.Load())

	outage = false
	s.RunOnce(t.Context())
	assert.Equal(int32(1), issueReads.Load())
	tracked := s.TrackedRepos()
	require.Len(t, tracked, 1)
	assert.Equal(platform.RepositoryIDKey(42), tracked[0].Key)
	assert.Len(events, 2)

	require.NoError(t, s.SetReposWithContext(t.Context(), nil, false))
	s.RunOnce(t.Context())
	require.Empty(t, s.TrackedRepos())
	outage = true
	readded := FallbackConfiguredRepoRefs(s.TrackedRepos(), raw)
	require.Len(t, readded, 1)
	assert.True(readded[0].Key.IsZero())
	require.NoError(t, s.SetReposWithContext(t.Context(), readded, false))
	s.RunOnce(t.Context())
	assert.Equal(int32(1), issueReads.Load())
	assert.Equal([][2]string{{"merge_requests", "auth"}, {"issues", "auth"}, {"merge_requests", "auth"}, {"issues", "auth"}}, events)
}

func TestSyncFailedArchivedMetadataRead(t *testing.T) {
	assert := assert.New(t)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com", Key: platform.RepositoryIDKey(42), Archived: true}
	client := &partialFailureMock{}
	metadataReads := 0
	client.getRepositoryByIDFn = func(_ context.Context, owner string, id int64) (*gh.Repository, error) {
		assert.Equal("acme", owner)
		assert.Equal(int64(42), id)
		metadataReads++
		return nil, platform.ErrPermissionDenied
	}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(1, metadataReads)
	assert.Empty(events)
}
