package github

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
	"go.kenn.io/forge/platform/gitealike"
	platformgitlab "go.kenn.io/forge/platform/gitlab"
)

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
		{"another repository starts failing", a, []observation{{a, platform.ErrNotFound}, {b, platform.ErrNotFound}}, []RepoRef{a, b}, b, 2, 2},
		{"no reads", a, nil, []RepoRef{a, b}, a, 1, 1},
		{"not modified", a, []observation{{a, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotModified}}}}, []RepoRef{a, b}, a, 1, 2},
		{"untracked without reads", a, nil, []RepoRef{b}, a, 1, 2},
		{"archived without reads", a, nil, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, a, 1, 2},
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

func TestSyncFailedIgnoredErrors(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"local path error", fmt.Errorf("read: %w", &fs.PathError{Op: "open", Path: "token", Err: fs.ErrNotExist})},
		{"local access denied", fmt.Errorf("read: %w", syscall.EACCES)},
		{"local operation denied", fmt.Errorf("read: %w", syscall.EPERM)},
		{"local full disk", fmt.Errorf("write: %w", syscall.ENOSPC)},
		{"local read-only disk", fmt.Errorf("write: %w", syscall.EROFS)},
		{"budget refused", platform.ErrSyncBudgetExhausted},
		{"disabled feature without cooldown", platform.RepositoryFeatureDisabled(platform.KindGitHub, "github.com", "issues", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pass := &syncPass{}
			pass.observe(repo, "refs", tc.err)
			var events [][2]string
			s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
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
		{"git 503", errors.New("fatal: unable to access remote: The requested URL returned error: 503"), "provider"},
		{"unknown", errors.New("private error details"), "other"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, syncFailureReason(fmt.Errorf("read: %w", tc.err))) })
	}
}

func TestSyncFailedGitLabNetworkErrors(t *testing.T) {
	for _, cause := range []error{
		&net.DNSError{Err: "unavailable", Name: "gitlab.example.com"},
		&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
		&net.OpError{Op: "read", Net: "tcp", Err: syscall.ETIMEDOUT},
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			var reads atomic.Int32
			client, err := platformgitlab.NewClient("gitlab.example.com", testTokenSource("test-token"),
				platformgitlab.WithoutRetriesForTesting(),
				platformgitlab.WithTransport(platform.RoundTripFunc(func(*http.Request) (*http.Response, error) {
					reads.Add(1)
					return nil, cause
				})))
			require.NoError(t, err)
			registry, err := platform.NewRegistry(client)
			require.NoError(t, err)
			repo := RepoRef{Platform: "gitlab", PlatformHost: "gitlab.example.com", Owner: "acme", Name: "widget"}
			s := NewSyncerWithRegistry(registry, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			require.Positive(t, reads.Load())
			assert.ElementsMatch(t, [][2]string{{"merge_requests", "network"}, {"issues", "network"}}, events)
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
		feature string
		failing bool
		want    [][2]string
	}{
		{"issues", "issues", false, [][2]string{{"merge_requests", "auth"}}},
		{"issues", "issues", true, nil},
		{"merge_requests", "merge_requests", false, [][2]string{{"issues", "auth"}}},
		{"merge_requests", "merge_requests", true, nil},
		// The detail drain skips CI reads under the merge request cooldown.
		{"checks", "merge_requests", true, nil},
	} {
		t.Run(fmt.Sprintf("%s/failing=%t", tc.source, tc.failing), func(t *testing.T) {
			assert := assert.New(t)
			source := tc.source
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			s := NewSyncer(map[string]Client{"github.com": &partialFailureMock{}}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
			now := time.Now().UTC()
			s.SetClock(func() time.Time { return now })
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			if tc.failing {
				pass := &syncPass{}
				pass.observe(repo, source, platform.ErrPermissionDenied)
				s.finishSyncPass(pass, []RepoRef{repo})
				require.Equal(t, [][2]string{{source, "auth"}}, events)
				events = nil
			}
			// Disablement can be discovered by an item refresh outside a full pass.
			require.True(t, s.recordRepositoryFeatureDisabled(repo, tc.feature, platform.RepositoryFeatureDisabled(platform.KindGitHub, repo.PlatformHost, tc.feature, errors.New("feature disabled"))))
			pass := &syncPass{}
			if !tc.failing {
				s.observeRepoFailure(context.WithValue(t.Context(), syncPassKey{}, pass), repo, platform.ErrPermissionDenied)
				assert.NotContains(pass.results, source, "repository failures must skip the disabled feature")
			}
			s.finishSyncPass(pass, []RepoRef{repo})
			assert.NotContains(s.syncFailures[source], mergeQueueRepoKey(repo))
			assert.Equal(tc.want, events)
			// A repository that leaves the cooldown still failing reports again.
			now = now.Add(repositoryFeatureProbeInterval)
			events = nil
			later := &syncPass{}
			later.observe(repo, source, platform.ErrPermissionDenied)
			s.finishSyncPass(later, []RepoRef{repo})
			assert.Equal([][2]string{{source, "auth"}}, events)
		})
	}
}

func TestSyncFailedGitCloneErrors(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	pass := &syncPass{}
	clones := gitclone.New(t.TempDir(), nil)
	t.Cleanup(clones.Wait)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	repo.PlatformHost = server.Listener.Addr().String()
	clones.SetAllowInsecureHTTP("github", repo.PlatformHost, true)
	repo.CloneURL = server.URL + "/acme/widget.git"
	s := NewSyncer(map[string]Client{"github.com": &mockClient{}}, openTestDB(t), clones, []RepoRef{repo}, time.Minute, nil, nil)
	err := s.ensureClone(context.WithValue(t.Context(), syncPassKey{}, pass), repo)
	require.ErrorContains(t, err, "git clone")
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.finishSyncPass(pass, []RepoRef{repo})
	assert.Equal(t, [][2]string{{"refs", "provider"}}, events)
}

func TestSyncFailedLockedRef(t *testing.T) {
	fixture := setupSyncBranchActivityFixture(t, "main")
	t.Cleanup(fixture.Syncer.clones.Wait)
	syncActivityCommitAndPush(t, fixture.Work, "seed.md", "seed\n", "seed", "main")
	ctx := withCloneRepositoryIdentity(t.Context(), fixture.Repo)
	require.NoError(t, fixture.Syncer.ensureClone(ctx, fixture.Repo))
	clone, err := fixture.Syncer.clones.ClonePathForContext(ctx, "gitlab", fixture.Repo.PlatformHost, fixture.Repo.Owner, fixture.Repo.Name)
	require.NoError(t, err)
	syncActivityCommitAndPush(t, fixture.Work, "seed.md", "updated\n", "update", "main")
	lock := filepath.Join(clone, "refs", "remotes", "origin", "main.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lock), 0o755))
	require.NoError(t, os.WriteFile(lock, nil, 0o600))
	_, stderr, err := gitsafe.Runner().Run(ctx, clone, nil, "fetch", "origin")
	require.Error(t, err)
	require.Contains(t, string(stderr), "refs/remotes/origin/main")
	var events [][2]string
	fixture.Syncer.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	fixture.Syncer.RunOnce(t.Context())
	assert.Empty(t, events)
}

func TestSyncFailedEmptyManagedGitLabToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key:        tokenauth.Key{Platform: "gitlab", Host: "gitlab.example.com"},
		Candidates: []tokenauth.Candidate{{Kind: tokenauth.SourceKindFile, FilePath: path}},
	}, tokenauth.Options{})
	client, err := platformgitlab.NewClient("gitlab.example.com", source,
		platformgitlab.WithoutRetriesForTesting(),
		platformgitlab.WithTransport(platform.RoundTripFunc(func(*http.Request) (*http.Response, error) {
			assert.Fail(t, "empty credentials must fail before sending an HTTP request")
			return nil, errors.New("unexpected request")
		})))
	require.NoError(t, err)
	registry, err := platform.NewRegistry(client)
	require.NoError(t, err)
	repo := RepoRef{Platform: "gitlab", PlatformHost: "gitlab.example.com", Owner: "acme", Name: "widget"}
	s := NewSyncerWithRegistry(registry, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.ElementsMatch(t, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
}

func TestSyncFailedMixedChecks(t *testing.T) {
	for _, failureFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("failureFirst=%t", failureFirst), func(t *testing.T) {
			first := observedPR(seededHead, "clean")
			s, client, d, repoID := newObservedGitHubSyncer(t, first)
			second := observedPR("second-head", "clean")
			second.Number = new(2)
			second.ID = new(int64(2000))
			client.openPRs = []*gh.PullRequest{first, second}
			client.getPullRequestFn = func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
				if number == 2 {
					return second, nil
				}
				return first, nil
			}
			seedObservedMR(t, d, repoID, "")
			seedObservedMR(t, d, repoID, "", func(mr *db.MergeRequest) {
				mr.Number, mr.PlatformID, mr.PlatformHeadSHA = 2, 2000, "second-head"
			})
			bucket, err := s.bucketKeyForRepo(observedGitHubRepo, false)
			require.NoError(t, err)
			s.budgets = map[string]*SyncBudget{bucket: NewSyncBudget(1000)}
			var reads []string
			client.listCheckRunsForRefFn = func(_ context.Context, _, _, ref string) ([]*gh.CheckRun, error) {
				reads = append(reads, ref)
				if (ref == seededHead) == failureFirst {
					return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusServiceUnavailable}}
				}
				return nil, nil
			}
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			require.ElementsMatch(t, []string{seededHead, "second-head"}, reads)
			assert.Empty(t, events)
		})
	}
}

func TestSyncFailedBitbucketDCUnavailable(t *testing.T) {
	var reads atomic.Int32
	client, err := bitbucketdc.NewClient("bitbucket.example.com", testTokenSource("test-token"), platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "/rest/api/latest/projects/ACME/repos/widget", req.URL.Path)
		reads.Add(1)
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Body: http.NoBody, Header: make(http.Header), Request: req}, nil
	}), nil)
	require.NoError(t, err)
	registry, err := platform.NewRegistry(client)
	require.NoError(t, err)
	repo := RepoRef{Platform: "bitbucket", PlatformHost: "bitbucket.example.com", Owner: "ACME", Name: "widget"}
	s := NewSyncerWithRegistry(registry, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	require.Positive(t, reads.Load())
	assert.Equal(t, [][2]string{{"merge_requests", "provider"}}, events)
}
