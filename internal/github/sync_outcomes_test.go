package github

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
	"go.kenn.io/forge/platform/gitealike"
)

func TestSyncFailedOncePerSource(t *testing.T) {
	a := RepoRef{Owner: "acme", Name: "a", PlatformHost: "github.com"}
	b := RepoRef{Owner: "acme", Name: "b", PlatformHost: "github.com"}
	client := &partialFailureMock{}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{a, b}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	for _, phase := range []struct {
		failA bool
		failB bool
		want  int
	}{
		{true, false, 1},
		{true, true, 1},
		{false, false, 1},
		{false, true, 2},
	} {
		var reads atomic.Int32
		client.listOpenIssuesFn = func(_ context.Context, owner, name string) ([]*gh.Issue, error) {
			assert.Equal(t, "acme", owner)
			assert.Contains(t, []string{"a", "b"}, name)
			reads.Add(1)
			if name == "a" && phase.failA || name == "b" && phase.failB {
				return nil, platform.ErrPermissionDenied
			}
			return nil, nil
		}
		s.RunOnce(t.Context())
		require.EqualValues(t, 2, reads.Load())
		require.Len(t, events, phase.want)
	}
	assert.Equal(t, [][2]string{{"issues", "auth"}, {"issues", "auth"}}, events)
}

func TestSyncFailedSourceRecovery(t *testing.T) {
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
		{"another repository starts failing", a, []observation{{a, platform.ErrNotFound}, {b, platform.ErrNotFound}}, []RepoRef{a, b}, b, 1, 1},
		{"cadence skipped with success", a, []observation{{b, nil}}, []RepoRef{a, b}, a, 1, 2},
		{"no reads", a, nil, []RepoRef{a, b}, a, 1, 1},
		{"disabled feature without cooldown", a, []observation{{a, platform.RepositoryFeatureDisabled(platform.KindGitHub, "github.com", "issues", nil)}}, []RepoRef{a, b}, a, 1, 1},
		{"not modified", a, []observation{{a, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotModified}}}}, []RepoRef{a, b}, a, 1, 2},
		{"failure dominates success", a, []observation{{a, platform.ErrNotFound}, {b, nil}}, []RepoRef{a, b}, a, 1, 1},
		{"budget refused", a, []observation{{a, platform.ErrSyncBudgetExhausted}}, []RepoRef{a, b}, a, 1, 1},
		{"budget refused with success", a, []observation{{a, platform.ErrSyncBudgetExhausted}, {b, nil}}, []RepoRef{a, b}, a, 1, 2},
		{"local path error", a, []observation{{a, fmt.Errorf("read: %w", &fs.PathError{Op: "open", Path: "token", Err: fs.ErrNotExist})}}, []RepoRef{a, b}, a, 1, 1},
		{"local access denied", a, []observation{{a, fmt.Errorf("read: %w", syscall.EACCES)}}, []RepoRef{a, b}, a, 1, 1},
		{"local operation denied", a, []observation{{a, fmt.Errorf("read: %w", syscall.EPERM)}}, []RepoRef{a, b}, a, 1, 1},
		{"local full disk", a, []observation{{a, fmt.Errorf("write: %w", syscall.ENOSPC)}}, []RepoRef{a, b}, a, 1, 1},
		{"local read-only disk", a, []observation{{a, fmt.Errorf("write: %w", syscall.EROFS)}}, []RepoRef{a, b}, a, 1, 1},
		{"untracked without reads", a, nil, []RepoRef{b}, a, 1, 1},
		{"archived without reads", a, nil, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, a, 1, 1},
		{"untracked", a, []observation{{b, nil}}, []RepoRef{b}, a, 1, 2},
		{"archived", a, []observation{{b, nil}}, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, a, 1, 2},
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
			// Resume reads from the later repository.
			s.finishSyncPass(later, []RepoRef{tc.later, b})
			assert.Len(t, events, tc.wantLater)
		})
	}
}

func TestSyncFailedSourcesRecoverIndependently(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget"}
	var events [][2]string
	s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
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
				s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
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
				pass.observe(other, "issues", nil)
				s.finishSyncPass(pass, []RepoRef{a, b})
				assert.Equal(t, [][2]string{{"issues", "auth"}}, events)
			})
		}
	}
}

func TestSyncFailedLocalFilesystemOnRefs(t *testing.T) {
	// A local file error on the refs source must classify as other, not stay silent.
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	pass := &syncPass{}
	pass.observe(repo, "refs", fmt.Errorf("clone: %w", &fs.PathError{Op: "mkdir", Path: "clone", Err: syscall.EACCES}))
	var events [][2]string
	s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
	s.finishSyncPass(pass, []RepoRef{repo})
	assert.Equal(t, [][2]string{{"refs", "other"}}, events)
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
	for _, tc := range []struct {
		failure error
		want    [][2]string
	}{
		{platform.ErrPermissionDenied, [][2]string{{"checks", "auth"}}},
		{platform.ErrNotFound, nil},
	} {
		t.Run(tc.failure.Error(), func(t *testing.T) {
			s, d, repoID := newObservedGitLabSyncer(t, observedGitLabMR(seededHead, "clean"), func(p *observingReadProvider) {
				p.checksErr = tc.failure
			})
			seedObservedMR(t, d, repoID, "")
			bucket, err := s.bucketKeyForRepo(observedGitLabRepo, false)
			require.NoError(t, err)
			s.budgets = map[string]*SyncBudget{bucket: NewSyncBudget(1000)}
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			assert.Equal(t, tc.want, events)
		})
	}
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
					assert.Equal(t, [][2]string{{"checks", "provider"}}, events)
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
		{"github", &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusServiceUnavailable, Request: req}}, "provider"},
		{"not found", platform.ErrNotFound, "not_found"},
		{"forgejo and gitea", &gitealike.HTTPError{StatusCode: 404}, "not_found"},
		{"bitbucket", &bitbucket.UnexpectedResponseStatusError{StatusCode: 429}, "rate_limited"},
		{"network", &net.DNSError{Err: "unavailable"}, "network"},
		{"deadline", context.DeadlineExceeded, "network"},
		{"contract", platform.ErrProviderContract, "provider"},
		{"authentication", platform.ErrPermissionDenied, "auth"},
		{"rate limit", platform.ErrRateLimited, "rate_limited"},
		{"normalized permission takes precedence", &platform.Error{Code: platform.ErrCodePermissionDenied, Err: &gitealike.HTTPError{StatusCode: 429}}, "auth"},
		{"normalized contract takes precedence", &platform.Error{Code: platform.ErrCodeProviderContract, Err: &gitealike.HTTPError{StatusCode: 404}}, "provider"},
		{"git DNS", errors.New("fatal: unable to access remote: Could not resolve host: example.com"), "network"},
		{"git refused", errors.New("fatal: unable to access remote: Connection refused"), "network"},
		{"git reset", errors.New("fatal: unable to access remote: Connection reset by peer"), "network"},
		{"git timeout", errors.New("fatal: unable to access remote: Operation timed out"), "network"},
		{"git 503", errors.New("fatal: unable to access remote: The requested URL returned error: 503"), "network"},
		{"unknown", errors.New("private error details"), "other"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, syncFailureReason(fmt.Errorf("read: %w", tc.err))) })
	}
}

func TestSyncFailureReasonBitbucketDC(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "auth"},
		{404, "not_found"},
		{429, "rate_limited"},
		{418, "other"},
		{500, "provider"},
		{503, "provider"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			client, err := bitbucketdc.NewClient("bitbucket.example.com", testTokenSource("test-token"), platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Status: http.StatusText(tc.status), Header: make(http.Header), Body: http.NoBody, Request: req}, nil
			}), nil)
			require.NoError(t, err)
			_, err = client.GetRepository(t.Context(), platform.RepoRef{Platform: platform.KindBitbucket, Host: "bitbucket.example.com", Owner: "PROJECT", Name: "widget", Key: platform.RepositoryIDKey(42)})
			require.Error(t, err)
			assert.Equal(t, tc.want, syncFailureReason(err))
		})
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

func TestSyncFailedRepositoryFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) *Syncer
	}{
		{"repository lookup", func(t *testing.T) *Syncer {
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			client := &partialFailureMock{}
			client.getRepositoryFn = func(context.Context, string, string) (*gh.Repository, error) {
				return nil, platform.ErrPermissionDenied
			}
			return NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
		}},
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
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.setup(t)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			assert.Equal(t, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
		})
	}
}

func TestSyncFailedDisabledFeatureCooldown(t *testing.T) {
	for _, tc := range []struct {
		source string
		// feature is the disabled feature whose cooldown applies to source.
		feature  string
		retained bool
		want     [][2]string
	}{
		{"issues", "issues", false, [][2]string{{"merge_requests", "auth"}}},
		{"issues", "issues", true, nil},
		{"merge_requests", "merge_requests", false, [][2]string{{"issues", "auth"}}},
		{"merge_requests", "merge_requests", true, nil},
		// The detail drain skips CI reads under the merge request cooldown.
		{"checks", "merge_requests", true, nil},
	} {
		t.Run(fmt.Sprintf("%s/retained=%t", tc.source, tc.retained), func(t *testing.T) {
			assert := assert.New(t)
			source := tc.source
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			s := NewSyncer(map[string]Client{"github.com": &partialFailureMock{}}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
			now := time.Now().UTC()
			s.SetClock(func() time.Time { return now })
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			if tc.retained {
				pass := &syncPass{}
				pass.observe(repo, source, platform.ErrPermissionDenied)
				s.finishSyncPass(pass, []RepoRef{repo})
				require.Equal(t, [][2]string{{source, "auth"}}, events)
				events = nil
			}
			// Disablement can be discovered by an item refresh outside a full pass.
			require.True(t, s.recordRepositoryFeatureDisabled(repo, tc.feature, platform.RepositoryFeatureDisabled(platform.KindGitHub, repo.PlatformHost, tc.feature, errors.New("feature disabled"))))
			pass := &syncPass{}
			if !tc.retained {
				s.observeRepoFailure(context.WithValue(t.Context(), syncPassKey{}, pass), repo, platform.ErrPermissionDenied)
				assert.NotContains(pass.results, source, "repository failures must skip the disabled feature")
			}
			s.finishSyncPass(pass, []RepoRef{repo})
			assert.Equal(tc.retained, s.syncFailures[source])
			assert.Equal(tc.want, events)
			// Resuming a failed probe continues the same source outage.
			now = now.Add(repositoryFeatureProbeInterval)
			events = nil
			later := &syncPass{}
			later.observe(repo, source, platform.ErrPermissionDenied)
			s.finishSyncPass(later, []RepoRef{repo})
			if tc.retained {
				assert.Empty(events)
			} else {
				assert.Equal([][2]string{{source, "auth"}}, events)
			}
		})
	}
}

func TestSyncFailedGitCloneErrors(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	pass := &syncPass{}
	clones := gitclone.New(t.TempDir(), nil)
	t.Cleanup(clones.Wait)
	repo.CloneURL = filepath.Join(t.TempDir(), "remote")
	s := NewSyncer(map[string]Client{"github.com": &mockClient{}}, openTestDB(t), clones, []RepoRef{repo}, time.Minute, nil, nil)
	err := s.ensureClone(context.WithValue(t.Context(), syncPassKey{}, pass), repo)
	require.ErrorContains(t, err, "git clone")
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.finishSyncPass(pass, []RepoRef{repo})
	assert.Equal(t, [][2]string{{"refs", "other"}}, events)
}
