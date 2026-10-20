package github

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
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
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
	"go.kenn.io/forge/platform/gitealike"
	platformgitlab "go.kenn.io/forge/platform/gitlab"
)

func TestSyncFailedRetainsUnreadRepositories(t *testing.T) {
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
		{"recovery overlaps new failure", a, []observation{{a, nil}, {b, platform.ErrNotFound}}, []RepoRef{a, b}, b, 1, 1},
		{"cadence skipped", a, []observation{{b, nil}}, []RepoRef{a, b}, a, 1, 1},
		{"budget refused", a, []observation{{a, platform.ErrSyncBudgetExhausted}, {b, nil}}, []RepoRef{a, b}, a, 1, 1},
		{"local path error", a, []observation{{a, fmt.Errorf("read: %w", &fs.PathError{Op: "open", Path: "token", Err: fs.ErrNotExist})}}, []RepoRef{a, b}, a, 1, 1},
		{"local access denied", a, []observation{{a, fmt.Errorf("read: %w", syscall.EACCES)}}, []RepoRef{a, b}, a, 1, 1},
		{"local operation denied", a, []observation{{a, fmt.Errorf("read: %w", syscall.EPERM)}}, []RepoRef{a, b}, a, 1, 1},
		{"local full disk", a, []observation{{a, fmt.Errorf("write: %w", syscall.ENOSPC)}}, []RepoRef{a, b}, a, 1, 1},
		{"local read-only disk", a, []observation{{a, fmt.Errorf("write: %w", syscall.EROFS)}}, []RepoRef{a, b}, a, 1, 1},
		{"disabled", a, []observation{{a, platform.ErrRepositoryFeatureDisabled}}, []RepoRef{a, b}, a, 1, 2},
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
			// Re-track and unarchive the later repository so its failure can emit.
			s.finishSyncPass(later, []RepoRef{tc.later, b})
			assert.Len(t, events, tc.wantLater)
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
	for _, tc := range []struct {
		status int
		want   string
	}{
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
	dcClient, err := bitbucketdc.NewClient("bitbucket.example.com", testTokenSource("test-token"), platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	}), nil)
	require.NoError(t, err)
	_, dcErr := dcClient.GetRepository(t.Context(), platform.RepoRef{Platform: platform.KindBitbucket, Host: "bitbucket.example.com", Owner: "PROJECT", Name: "widget", Key: platform.RepositoryIDKey(42)})
	require.Error(t, dcErr)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"github primary limit", &gh.RateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden, Request: req}}, "rate_limited"},
		{"github secondary limit", &gh.AbuseRateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden, Request: req}}, "rate_limited"},
		{"github", &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusServiceUnavailable, Request: req}}, "provider"},
		{"forgejo and gitea", &gitealike.HTTPError{StatusCode: 404}, "not_found"},
		{"bitbucket data center", dcErr, "provider"},
		{"bitbucket", &bitbucket.UnexpectedResponseStatusError{StatusCode: 429}, "rate_limited"},
		{"network", &net.DNSError{Err: "unavailable"}, "network"},
		{"deadline", context.DeadlineExceeded, "network"},
		{"contract", platform.ErrProviderContract, "provider"},
		{"authentication", platform.ErrPermissionDenied, "auth"},
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

// The list excludes the seeded item, so its read comes from closure reconciliation.
type closureFailureProvider struct {
	*syncTestReadProvider
	failure error
}

func (p *closureFailureProvider) GetMergeRequest(_ context.Context, _ platform.RepoRef, _ int) (platform.MergeRequest, error) {
	p.getMRCalls.Add(1)
	return platform.MergeRequest{}, p.failure
}

func TestSyncFailedFullPassDetail(t *testing.T) {
	for _, tc := range []struct {
		name string
		// detail refreshes cached open items; closure reconciles items the list omits;
		// neutral closure does the same through a provider without GitHub's classified lookup.
		path   string
		source string
		status int
		// probe fails one repository probe per item read; zero leaves them passing.
		probe int
		want  [][2]string
	}{
		{"provider failure/issue detail", "detail", "issues", http.StatusServiceUnavailable, 0, [][2]string{{"issues", "provider"}}},
		{"provider failure/PR detail", "detail", "merge_requests", http.StatusServiceUnavailable, 0, [][2]string{{"merge_requests", "provider"}}},
		{"missing item/issue detail", "detail", "issues", http.StatusNotFound, 0, nil},
		{"missing item/PR detail", "detail", "merge_requests", http.StatusNotFound, 0, nil},
		{"deleted issue/issue detail", "detail", "issues", http.StatusGone, 0, nil},
		{"missing items/closure", "closure", "", http.StatusNotFound, 0, nil},
		{"deleted items/closure", "closure", "", http.StatusGone, 0, [][2]string{{"merge_requests", "other"}}},
		{"provider failure/closure", "closure", "", http.StatusServiceUnavailable, 0, [][2]string{{"merge_requests", "provider"}, {"issues", "provider"}}},
		{"inaccessible PR/PR detail", "detail", "merge_requests", http.StatusForbidden, 0, [][2]string{{"merge_requests", "auth"}}},
		{"inaccessible items/closure", "closure", "", http.StatusForbidden, 0, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}},
		{"missing PR then probe failure/PR detail", "detail", "merge_requests", http.StatusNotFound, http.StatusServiceUnavailable, [][2]string{{"merge_requests", "provider"}}},
		{"missing items then probe failure/closure", "closure", "", http.StatusNotFound, http.StatusServiceUnavailable, [][2]string{{"merge_requests", "provider"}, {"issues", "provider"}}},
		{"missing items/neutral closure", "neutral closure", "", http.StatusNotFound, 0, nil},
		{"provider failure/neutral closure", "neutral closure", "", http.StatusServiceUnavailable, 0, [][2]string{{"merge_requests", "provider"}, {"issues", "provider"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			neutral := tc.path == "neutral closure"
			if neutral {
				repo = RepoRef{Platform: platform.KindGitLab, PlatformHost: "gitlab.example.com", Owner: "acme", Name: "widget"}
			} else {
				repo.Key = platform.RepositoryIDKey(testRepoID(repo.Owner, repo.Name))
			}
			id, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
			require.NoError(t, err)
			now := time.Now().UTC()
			if tc.source != "merge_requests" {
				_, err = d.UpsertIssue(t.Context(), &db.Issue{RepoID: id, PlatformID: 7, Number: 7, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now, CommentCount: 100})
				require.NoError(t, err)
			}
			if tc.source != "issues" {
				_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: id, PlatformID: 8, Number: 8, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now, CommentCount: 100, MergeableState: "clean"})
				require.NoError(t, err)
			}
			var issueCalls, mrCalls atomic.Int32
			issueReads, mrReads := issueCalls.Load, mrCalls.Load
			var s *Syncer
			if neutral {
				failure := &gitealike.HTTPError{StatusCode: tc.status}
				provider := &closureFailureProvider{syncTestReadProvider: &syncTestReadProvider{kind: repo.Platform, host: repo.PlatformHost, getIssueErr: failure}, failure: failure}
				registry, err := platform.NewRegistry(provider)
				require.NoError(t, err)
				s = NewSyncerWithRegistry(registry, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
				issueReads, mrReads = provider.getIssueCalls.Load, provider.getMRCalls.Load
			} else {
				client := &partialFailureMock{issuesCached: true, prsCached: true}
				if tc.path == "closure" {
					client = &partialFailureMock{}
				}
				failure := &gh.ErrorResponse{Response: &http.Response{StatusCode: tc.status}}
				if tc.source != "merge_requests" {
					client.getIssueFn = func(_ context.Context, owner, name string, number int) (*gh.Issue, error) {
						assert.Equal(t, "acme/widget/7", fmt.Sprintf("%s/%s/%d", owner, name, number))
						issueCalls.Add(1)
						return nil, failure
					}
				}
				if tc.source != "issues" {
					client.getPullRequestFn = func(_ context.Context, owner, name string, number int) (*gh.PullRequest, error) {
						assert.Equal(t, "acme/widget/8", fmt.Sprintf("%s/%s/%d", owner, name, number))
						mrCalls.Add(1)
						return nil, failure
					}
				}
				var probes atomic.Int32
				if tc.probe != 0 {
					client.getRepositoryFn = func(context.Context, string, string) (*gh.Repository, error) {
						if probes.Load() < issueCalls.Load()+mrCalls.Load() {
							probes.Add(1)
							return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: tc.probe}}
						}
						return &gh.Repository{ID: new(testRepoID(repo.Owner, repo.Name)), Owner: &gh.User{Login: &repo.Owner}, Name: &repo.Name}, nil
					}
				}
				s = NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
			}
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			if tc.source != "merge_requests" {
				require.Positive(t, issueReads(), "the refresh must reach the failing issue read")
			}
			if tc.source != "issues" {
				require.Positive(t, mrReads(), "the refresh must reach the failing PR read")
			}
			assert.Equal(t, tc.want, events)
		})
	}
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
		source   string
		retained bool
		want     [][2]string
	}{
		{"issues", false, [][2]string{{"merge_requests", "auth"}}},
		{"issues", true, nil},
		{"merge_requests", false, [][2]string{{"issues", "auth"}}},
		{"merge_requests", true, nil},
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
			require.True(t, s.recordRepositoryFeatureDisabled(repo, source, platform.RepositoryFeatureDisabled(platform.KindGitHub, repo.PlatformHost, source, errors.New("feature disabled"))))
			pass := &syncPass{}
			if !tc.retained {
				s.observeRepoFailure(context.WithValue(t.Context(), syncPassKey{}, pass), repo, platform.ErrPermissionDenied)
				assert.NotContains(pass.results, source, "repository failures must skip the disabled feature")
			}
			s.finishSyncPass(pass, []RepoRef{repo})
			assert.NotContains(s.syncFailures[source], mergeQueueRepoKey(repo))
			assert.Equal(tc.want, events)
			// Once probing resumes, a fresh failure must count as a new outage.
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
	for _, operation := range []string{"clone", "fetch", "local filesystem"} {
		t.Run(operation, func(t *testing.T) {
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			pass := &syncPass{}
			var s *Syncer
			if operation == "local filesystem" {
				// A local file error on the refs source must classify as other, not stay silent.
				s = &Syncer{}
				pass.observe(repo, "refs", fmt.Errorf("clone: %w", &fs.PathError{Op: "mkdir", Path: "clone", Err: syscall.EACCES}))
			} else {
				remote := filepath.Join(t.TempDir(), "remote")
				if operation == "fetch" {
					remote = gitfixture.NewRepository(t, false).Dir
				}
				clones := gitclone.New(t.TempDir(), nil)
				t.Cleanup(clones.Wait)
				repo.CloneURL = remote
				s = NewSyncer(map[string]Client{"github.com": &mockClient{}}, openTestDB(t), clones, []RepoRef{repo}, time.Minute, nil, nil)
				if operation == "fetch" {
					require.NoError(t, s.ensureClone(t.Context(), repo))
					require.NoError(t, os.Rename(filepath.Join(remote, ".git"), filepath.Join(remote, ".git-unavailable")))
				}
				err := s.ensureClone(context.WithValue(t.Context(), syncPassKey{}, pass), repo)
				require.ErrorContains(t, err, "git "+operation)
			}
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.finishSyncPass(pass, []RepoRef{repo})
			assert.Equal(t, [][2]string{{"refs", "other"}}, events)
		})
	}
}
