package github

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

func TestSyncFailedSourceRecovery(t *testing.T) {
	a := RepoRef{Owner: "acme", Name: "a"}
	b := RepoRef{Owner: "acme", Name: "b"}
	resolved := a
	resolved.Key = platform.RepositoryIDKey(42)
	renamed := resolved
	renamed.Name = "renamed"
	initialFailure := [][2]string{{"issues", "not_found"}}
	additionalFailure := [][2]string{{"issues", "not_found"}, {"issues", "not_found"}}
	type observation struct {
		repo RepoRef
		err  error
	}
	for _, tc := range []struct {
		name                  string
		first                 RepoRef
		next                  []observation
		tracked               []RepoRef
		later                 RepoRef
		wantAfterNextPass     [][2]string
		wantAfterLaterFailure [][2]string
	}{
		{"another repository starts failing", a, []observation{{a, platform.ErrNotFound}, {b, platform.ErrNotFound}}, []RepoRef{a, b}, b, additionalFailure, additionalFailure},
		{"no reads", a, nil, []RepoRef{a, b}, a, initialFailure, initialFailure},
		{"not modified", a, []observation{{a, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotModified}}}}, []RepoRef{a, b}, a, initialFailure, additionalFailure},
		{"resolved identity keeps outage", a, []observation{{resolved, platform.ErrNotFound}}, []RepoRef{resolved}, resolved, initialFailure, initialFailure},
		{"rename keeps identity", resolved, []observation{{renamed, platform.ErrSyncBudgetExhausted}}, []RepoRef{renamed}, renamed, initialFailure, initialFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events [][2]string
			s := NewSyncer(nil, openTestDB(t), nil, nil, time.Minute, nil, nil)
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			first := &syncPass{}
			first.observe(tc.first, "issues", platform.ErrNotFound)
			s.finishSyncPass(first, []RepoRef{tc.first, b})
			second := &syncPass{}
			for _, observation := range tc.next {
				second.observe(observation.repo, "issues", observation.err)
			}
			s.finishSyncPass(second, tc.tracked)
			require.Equal(t, tc.wantAfterNextPass, events)
			later := &syncPass{}
			later.observe(tc.later, "issues", platform.ErrNotFound)
			// Resume reads from the later repository.
			s.finishSyncPass(later, []RepoRef{tc.later, b})
			assert.Equal(t, tc.wantAfterLaterFailure, events)
		})
	}
}

func TestSyncFailedSourcesRecoverIndependently(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget"}
	var events [][2]string
	s := NewSyncer(nil, openTestDB(t), nil, nil, time.Minute, nil, nil)
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	for _, recovered := range []bool{false, true, false} {
		pass := &syncPass{}
		pass.observe(repo, "issues", platform.ErrPermissionDenied)
		var checksErr error
		if !recovered {
			checksErr = platform.ErrPermissionDenied
		}
		pass.observe(repo, "checks", checksErr)
		s.finishSyncPass(pass, []RepoRef{repo})
	}
	assert.Equal(t, [][2]string{{"issues", "auth"}, {"checks", "auth"}, {"checks", "auth"}}, events)
}

func TestSyncFailedReasonPriority(t *testing.T) {
	a := RepoRef{Owner: "acme", Name: "a"}
	b := RepoRef{Owner: "acme", Name: "b"}
	for _, acrossRepos := range []bool{false, true} {
		for _, authFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("acrossRepos=%t/authFirst=%t", acrossRepos, authFirst), func(t *testing.T) {
				var events [][2]string
				s := NewSyncer(nil, openTestDB(t), nil, nil, time.Minute, nil, nil)
				s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
				var first, second error = errors.New("unknown failure"), platform.ErrPermissionDenied
				if authFirst {
					first, second = second, first
				}
				pass := &syncPass{}
				pass.observe(a, "issues", first)
				other := a
				if acrossRepos {
					other = b
				}
				pass.observe(other, "issues", second)
				s.finishSyncPass(pass, []RepoRef{a, b})
				assert.Equal(t, [][2]string{{"issues", "auth"}}, events)
			})
		}
	}
}

func TestSyncFailedIgnoredErrors(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
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
		{"disabled feature without cooldown", "issues", platform.RepositoryFeatureDisabled(platform.KindGitHub, "github.com", "issues", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pass := &syncPass{}
			pass.observe(repo, tc.source, tc.err)
			var events [][2]string
			s := NewSyncer(nil, openTestDB(t), nil, nil, time.Minute, nil, nil)
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.finishSyncPass(pass, []RepoRef{repo})
			assert.Empty(t, events)
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

func TestSyncFailedGitHubChecksMissingItem(t *testing.T) {
	for _, endpoint := range []string{"check runs", "combined status"} {
		t.Run(endpoint, func(t *testing.T) {
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			client := &mockClient{}
			var events [][2]string
			s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			for i, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
				failure := &gh.ErrorResponse{Response: &http.Response{StatusCode: status}}
				if endpoint == "check runs" {
					client.checkRunsErr = failure
				} else {
					client.ciStatusErr = failure
				}
				pass := &syncPass{}
				ctx := context.WithValue(t.Context(), syncPassKey{}, pass)
				_, err := s.fetchGitHubCIStatus(ctx, repo, 7, "head")
				require.NoError(t, err)
				s.finishSyncPass(pass, []RepoRef{repo})
				if i == 0 {
					assert.Empty(t, events)
				} else {
					assert.Equal(t, [][2]string{{"checks", "other"}}, events)
				}
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
	require.NoError(t, s.syncOpenMRFromBulk(ctx, repo, id, time.Time{}, &BulkPR{
		PR: buildOpenPR(7, time.Now().UTC()), CIComplete: true,
	}, false))
	s.finishSyncPass(pass, []RepoRef{repo})
	assert.Empty(t, events)
}

func TestSyncFailedRepositoryFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T) *Syncer
		reason string
	}{
		{"repository lookup", func(t *testing.T) *Syncer {
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			d := openTestDB(t)
			_, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
			require.NoError(t, err)
			client := &partialFailureMock{}
			client.getRepositoryFn = func(_ context.Context, owner, name string) (*gh.Repository, error) {
				assert.Equal(t, "acme", owner)
				assert.Equal(t, "widget", name)
				return nil, platform.ErrPermissionDenied
			}
			return NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
		}, "auth"},
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

func TestSyncFailedMixedReads(t *testing.T) {
	for _, failureFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("failureFirst=%t", failureFirst), func(t *testing.T) {
			repo := RepoRef{Owner: "acme", Name: "widget"}
			var events [][2]string
			s := NewSyncer(nil, openTestDB(t), nil, nil, time.Minute, nil, nil)
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			pass := &syncPass{}
			var first, second error = nil, platform.ErrPermissionDenied
			if failureFirst {
				first, second = second, first
			}
			pass.observe(repo, "issues", first)
			pass.observe(repo, "issues", second)
			s.finishSyncPass(pass, []RepoRef{repo})
			assert.Empty(t, events)
		})
	}
}
