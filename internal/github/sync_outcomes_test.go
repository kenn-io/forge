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
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/procutil"
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
		{"untracked without reads", a, nil, []RepoRef{b}, a, initialFailure, additionalFailure},
		{"archived without reads", a, nil, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, a, initialFailure, additionalFailure},
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
		{"disabled feature without cooldown", "issues", platform.RepositoryFeatureDisabled(platform.KindGitHub, "github.com", "issues", nil)},
		{"refs token file error", "refs", fmt.Errorf("%w: %w", gitclone.ErrCredentialUnavailable, &fs.PathError{Op: "open", Path: "token", Err: fs.ErrNotExist})},
		{"refs deadline only", "refs", fmt.Errorf("resolve git token: %w", context.DeadlineExceeded)},
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
		{"bitbucket DC", &bitbucketdc.HTTPError{StatusCode: 503}, "provider"},
		{"network", &net.DNSError{Err: "unavailable"}, "network"},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, "network"},
		{"connection timeout", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ETIMEDOUT}, "network"},
		{"deadline", context.DeadlineExceeded, "network"},
		{"contract", platform.ErrProviderContract, "provider"},
		{"authentication", platform.ErrPermissionDenied, "auth"},
		{"rate limit", platform.ErrRateLimited, "rate_limited"},
		{"normalized permission takes precedence", &platform.Error{Code: platform.ErrCodePermissionDenied, Err: &gitealike.HTTPError{StatusCode: 429}}, "auth"},
		{"normalized contract takes precedence", &platform.Error{Code: platform.ErrCodeProviderContract, Err: &gitealike.HTTPError{StatusCode: 404}}, "provider"},
		{"unknown", errors.New("private error details"), "other"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, syncFailureReason(fmt.Errorf("read: %w", tc.err))) })
	}
	t.Run("git", func(t *testing.T) {
		cases := []struct {
			name string
			err  error
			want string
		}{
			{"nil", nil, ""},
			{"git refused", errors.New("fatal: unable to access remote: Connection refused"), "network"},
			{"git timeout", errors.New("fatal: unable to access remote: Operation timed out"), "network"},
			{"github 500 with internal server error preamble", fmt.Errorf("exit status 128: %s", "remote: Internal Server Error\nfatal: unable to access 'https://github.com/x/y.git/': The requested URL returned error: 500"), "provider"},
			{"bare 502", errors.New("fatal: The requested URL returned error: 502"), "provider"},
			{"bare 503", errors.New("fatal: The requested URL returned error: 503"), "provider"},
			{"bare 504", errors.New("fatal: The requested URL returned error: 504"), "provider"},
			{"HTTP 401", errors.New("fatal: The requested URL returned error: 401"), "auth"},
			{"HTTP 403", errors.New("fatal: The requested URL returned error: 403"), "auth"},
			{"HTTP 429", errors.New("fatal: The requested URL returned error: 429"), "rate_limited"},
			{"certificate verification", errors.New("fatal: unable to access remote: server certificate verification failed"), "network"},
			{"SSL certificate problem", errors.New("fatal: unable to access remote: SSL certificate problem: self-signed certificate"), "network"},
			{"HTTP 501", errors.New("fatal: The requested URL returned error: 501"), "provider"},
			{"connection reset", errors.New("error: RPC failed; curl 56 Recv failure: Connection reset by peer"), "network"},
			{"stalled HTTP transfer", errors.New("fatal: unable to access 'https://example.com/repo.git/': Operation too slow. Less than 1 bytes/sec transferred the last 30 seconds"), "network"},
			{"could not resolve host", errors.New("fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com"), "network"},
			{"case insensitive", errors.New("REMOTE: INTERNAL SERVER ERROR"), "provider"},
			{"early eof", errors.New("fatal: early EOF\nfatal: index-pack failed"), "network"},
			{"auth failure", errors.New("fatal: Authentication failed for 'https://github.com/x/y.git/'"), "auth"},
			{"not found", errors.New("fatal: repository 'https://github.com/x/y.git/' not found"), "not_found"},
			{"bad ref", errors.New("fatal: couldn't find remote ref refs/heads/nope"), ""},
			{"HTTP 404", errors.New("fatal: The requested URL returned error: 404"), "not_found"},
			{"HTTP 410", errors.New("fatal: The requested URL returned error: 410"), "not_found"},
			{"locked ref", errors.New("error: cannot lock ref 'refs/remotes/origin/main': Unable to create 'refs/remotes/origin/main.lock': File exists"), ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, gitclone.FailureReason(tc.err))
			})
		}
	})
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
			}
			s.finishSyncPass(pass, []RepoRef{repo})
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

func TestSyncFailedGitHTTP(t *testing.T) {
	for _, tc := range []struct {
		status     int
		reason     string
		emptyToken bool
		emptyReply bool
	}{
		{http.StatusForbidden, "auth", false, false},
		{0, "auth", true, false},
		{0, "network", false, true},
	} {
		name := fmt.Sprint(tc.status)
		if tc.emptyToken {
			name = "empty managed token"
		} else if tc.emptyReply {
			name = "empty reply"
		}
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			fixture := setupSyncBranchActivityFixture(t, "main")
			syncActivityCommitAndPush(t, fixture.Work, "seed.md", "seed\n", "seed", "main")
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				assert.Equal("/acme/widget.git/info/refs", req.URL.Path)
				assert.Equal("git-upload-pack", req.URL.Query().Get("service"))
				reads.Add(1)
				if tc.emptyReply {
					conn, _, err := w.(http.Hijacker).Hijack()
					if assert.NoError(err) {
						assert.NoError(conn.Close())
					}
					return
				}
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(server.Close)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: server.Listener.Addr().String(), CloneURL: server.URL + "/acme/widget.git"}
			remote := repo.CloneURL
			client := &mockClient{getRepositoryFn: func(_ context.Context, owner, name string) (*gh.Repository, error) {
				return &gh.Repository{ID: new(int64(42)), Owner: &gh.User{Login: new(owner)}, Name: new(name), CloneURL: new(remote), DefaultBranch: new("main")}, nil
			}}
			sources := gitclone.HostSources{}
			var tokenFile string
			if tc.emptyToken {
				tokenFile = filepath.Join(t.TempDir(), "token")
				require.NoError(t, os.WriteFile(tokenFile, nil, 0o600))
				sources[repo.PlatformHost] = tokenauth.NewManagedSource(tokenauth.Descriptor{
					Key:        tokenauth.Key{Platform: "github", Host: repo.PlatformHost},
					Candidates: []tokenauth.Candidate{{Kind: tokenauth.SourceKindFile, FilePath: tokenFile}},
				}, tokenauth.Options{})
			}
			clones := gitclone.New(t.TempDir(), sources)
			t.Cleanup(clones.Wait)
			clones.SetAllowInsecureHTTP("github", repo.PlatformHost, true)
			s := NewSyncer(map[string]Client{repo.PlatformHost: client}, openTestDB(t), clones, []RepoRef{repo}, time.Minute, nil, nil)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			assert.Equal([][2]string{{"refs", tc.reason}}, events)
			s.RunOnce(t.Context())
			assert.Len(events, 1)
			if tc.emptyToken {
				assert.Zero(reads.Load(), "missing credentials must fail before Git contacts the remote")
				require.NoError(t, os.WriteFile(tokenFile, []byte("test-token"), 0o600))
			}
			remote = fixture.Remote
			s.RunOnce(t.Context())
			assert.Len(events, 1)
			repo.Key = platform.RepositoryIDKey(42)
			clone, err := clones.ClonePathForContext(withCloneRepositoryIdentity(t.Context(), repo), "github", repo.PlatformHost, repo.Owner, repo.Name)
			require.NoError(t, err)
			syncActivityGitRun(t, clone, "remote", "set-url", "origin", repo.CloneURL)
			remote = repo.CloneURL
			if tc.emptyToken {
				require.NoError(t, os.WriteFile(tokenFile, nil, 0o600))
			}
			s.RunOnce(t.Context())
			assert.Equal([][2]string{{"refs", tc.reason}, {"refs", tc.reason}}, events)
			if tc.emptyToken {
				assert.Zero(reads.Load())
			}
		})
	}
}

func TestSyncFailedRefsProcessCapacity(t *testing.T) {
	assert := assert.New(t)
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: server.Listener.Addr().String(), CloneURL: server.URL + "/acme/widget.git"}
	client := &mockClient{getRepositoryFn: func(_ context.Context, owner, name string) (*gh.Repository, error) {
		return &gh.Repository{ID: new(int64(42)), Owner: &gh.User{Login: new(owner)}, Name: new(name), CloneURL: new(repo.CloneURL), DefaultBranch: new("main")}, nil
	}}
	clones := gitclone.New(t.TempDir(), nil)
	t.Cleanup(clones.Wait)
	clones.SetAllowInsecureHTTP("github", repo.PlatformHost, true)
	s := NewSyncer(map[string]Client{repo.PlatformHost: client}, openTestDB(t), clones, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	limiter := procutil.NewLimiterWithAcquireTimeout(1, time.Millisecond)
	t.Cleanup(procutil.SetDefaultLimiterForTest(limiter))
	release, err := limiter.TryAcquire(t.Context(), "test")
	require.NoError(t, err)
	t.Cleanup(release)
	s.RunOnce(t.Context())
	assert.Empty(events)
	assert.Zero(reads.Load(), "capacity exhaustion must prevent Git from starting")
	release()
	s.RunOnce(t.Context())
	assert.Positive(reads.Load())
	assert.Equal([][2]string{{"refs", "provider"}}, events)
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
	var events []string
	s.SetOnSyncFailed(func(source, _ string) { events = append(events, source) })
	s.RunOnce(t.Context())
	require.Positive(t, reads.Load())
	assert.Equal(t, []string{"merge_requests"}, events)
}
