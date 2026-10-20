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
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/tokenauth"
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
		{"persistent failure overlaps new failure", a, []observation{{a, platform.ErrNotFound}, {b, platform.ErrNotFound}}, []RepoRef{a, b}, b, 1, 1},
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

func TestSyncFailedFullPassDetail(t *testing.T) {
	for _, tc := range []struct {
		name string
		// detail refreshes cached open items; closure reconciles items the list omits.
		path   string
		source string
		status int
		want   [][2]string
	}{
		{"provider failure/issue detail", "detail", "issues", http.StatusServiceUnavailable, [][2]string{{"issues", "provider"}}},
		{"provider failure/PR detail", "detail", "merge_requests", http.StatusServiceUnavailable, [][2]string{{"merge_requests", "provider"}}},
		{"missing item/issue detail", "detail", "issues", http.StatusNotFound, nil},
		{"missing item/PR detail", "detail", "merge_requests", http.StatusNotFound, nil},
		{"deleted issue/issue detail", "detail", "issues", http.StatusGone, nil},
		{"missing items/closure", "closure", "", http.StatusNotFound, nil},
		{"deleted items/closure", "closure", "", http.StatusGone, [][2]string{{"merge_requests", "other"}}},
		{"provider failure/closure", "closure", "", http.StatusServiceUnavailable, [][2]string{{"merge_requests", "provider"}, {"issues", "provider"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			repo.Key = platform.RepositoryIDKey(testRepoID(repo.Owner, repo.Name))
			id, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
			require.NoError(t, err)
			now := time.Now().UTC()
			client := &partialFailureMock{issuesCached: true, prsCached: true}
			if tc.path == "closure" {
				client = &partialFailureMock{}
			}
			failure := &gh.ErrorResponse{Response: &http.Response{StatusCode: tc.status}}
			var issueCalls, mrCalls atomic.Int32
			if tc.source != "merge_requests" {
				_, err = d.UpsertIssue(t.Context(), &db.Issue{RepoID: id, PlatformID: 7, Number: 7, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now, CommentCount: 100})
				require.NoError(t, err)
				client.getIssueFn = func(_ context.Context, owner, name string, number int) (*gh.Issue, error) {
					assert.Equal(t, "acme/widget/7", fmt.Sprintf("%s/%s/%d", owner, name, number))
					issueCalls.Add(1)
					return nil, failure
				}
			}
			if tc.source != "issues" {
				_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: id, PlatformID: 8, Number: 8, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now, CommentCount: 100, MergeableState: "clean"})
				require.NoError(t, err)
				client.getPullRequestFn = func(_ context.Context, owner, name string, number int) (*gh.PullRequest, error) {
					assert.Equal(t, "acme/widget/8", fmt.Sprintf("%s/%s/%d", owner, name, number))
					mrCalls.Add(1)
					return nil, failure
				}
			}
			s := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			if tc.source != "merge_requests" {
				require.Positive(t, issueCalls.Load(), "the refresh must reach the failing issue read")
			}
			if tc.source != "issues" {
				require.Positive(t, mrCalls.Load(), "the refresh must reach the failing PR read")
			}
			assert.Equal(t, tc.want, events)
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

func TestSyncFailedUnreadableCredentialFile(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	parent := filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.WriteFile(parent, nil, 0o600))
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key:        tokenauth.Key{Platform: "github", Host: "github.com"},
		Candidates: []tokenauth.Candidate{{Kind: tokenauth.SourceKindFile, FilePath: filepath.Join(parent, "token")}},
	}, tokenauth.Options{})
	client, err := NewClient(source, "github.com", nil, nil, WithBaseURLForTesting(server.URL))
	require.NoError(t, err)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	var results []RepoSyncResult
	s.SetOnSyncCompleted(func(got []RepoSyncResult) { results = got })
	s.RunOnce(t.Context())
	require.Len(t, results, 1)
	require.Contains(t, results[0].Error, parent)
	assert.Zero(t, calls.Load())
	assert.Empty(t, events)
	for _, source := range []string{"merge_requests", "issues"} {
		assert.False(t, s.syncFailures[source][mergeQueueRepoKey(repo)])
	}
}

func TestSyncFailedDisabledFeatureCooldown(t *testing.T) {
	for _, source := range []string{"issues", "merge_requests"} {
		for _, retained := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retained=%t", source, retained), func(t *testing.T) {
				assert := assert.New(t)
				repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
				s := NewSyncer(map[string]Client{"github.com": &partialFailureMock{}}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
				now := time.Now().UTC()
				s.SetClock(func() time.Time { return now })
				var events [][2]string
				s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
				if retained {
					pass := &syncPass{}
					pass.observe(repo, source, platform.ErrPermissionDenied)
					s.finishSyncPass(pass, []RepoRef{repo})
					require.Equal(t, [][2]string{{source, "auth"}}, events)
					events = nil
				}
				// Disablement can be discovered by an item refresh outside a full pass.
				require.True(t, s.recordRepositoryFeatureDisabled(repo, source, platform.RepositoryFeatureDisabled(platform.KindGitHub, repo.PlatformHost, source, errors.New("feature disabled"))))
				pass := &syncPass{}
				if !retained {
					s.observeRepoFailure(context.WithValue(t.Context(), syncPassKey{}, pass), repo, platform.ErrPermissionDenied)
					assert.NotContains(pass.results, source, "repository failures must skip the disabled feature")
				}
				s.finishSyncPass(pass, []RepoRef{repo})
				assert.NotContains(s.syncFailures[source], mergeQueueRepoKey(repo))
				if retained {
					assert.Empty(events)
				} else if source == "issues" {
					assert.Equal([][2]string{{"merge_requests", "auth"}}, events)
				} else {
					assert.Equal([][2]string{{"issues", "auth"}}, events)
				}
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
}

func TestSyncFailedRefsClassification(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget"}
	var events [][2]string
	s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
	pass := &syncPass{}
	pass.observe(repo, "refs", fmt.Errorf("clone: %w", &fs.PathError{Op: "mkdir", Path: "clone", Err: syscall.EACCES}))
	s.finishSyncPass(pass, []RepoRef{repo})
	assert.Equal(t, [][2]string{{"refs", "other"}}, events)
}

func TestSyncFailedGitCloneErrors(t *testing.T) {
	for _, operation := range []string{"clone", "fetch"} {
		t.Run(operation, func(t *testing.T) {
			remote := filepath.Join(t.TempDir(), "remote")
			if operation == "fetch" {
				remote = gitfixture.NewRepository(t, false).Dir
			}
			clones := gitclone.New(t.TempDir(), nil)
			t.Cleanup(clones.Wait)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com", CloneURL: remote}
			s := NewSyncer(map[string]Client{"github.com": &mockClient{}}, openTestDB(t), clones, []RepoRef{repo}, time.Minute, nil, nil)
			if operation == "fetch" {
				require.NoError(t, s.ensureClone(t.Context(), repo))
				require.NoError(t, os.Rename(filepath.Join(remote, ".git"), filepath.Join(remote, ".git-unavailable")))
			}
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			pass := &syncPass{}
			err := s.ensureClone(context.WithValue(t.Context(), syncPassKey{}, pass), repo)
			require.ErrorContains(t, err, "git "+operation)
			s.finishSyncPass(pass, []RepoRef{repo})
			assert.Equal(t, [][2]string{{"refs", "other"}}, events)
		})
	}
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

func TestSyncFailedClosureReads(t *testing.T) {
	for _, status := range []int{404, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client, err := platformgitlab.NewClient("gitlab.example.com", testTokenSource("test-token"), platformgitlab.WithoutRetriesForTesting(), platformgitlab.WithTransport(platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
			})))
			require.NoError(t, err)
			ref := platform.RepoRef{Owner: "acme", Name: "widget", Key: platform.RepositoryIDKey(42)}
			_, issueErr := client.GetIssue(t.Context(), ref, 7)
			require.Error(t, issueErr)
			_, mrErr := client.GetMergeRequest(t.Context(), ref, 8)
			require.Error(t, mrErr)

			d := openTestDB(t)
			repo := RepoRef{Platform: platform.KindGitLab, PlatformHost: "gitlab.example.com", Owner: "acme", Name: "widget"}
			id, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
			require.NoError(t, err)
			now := time.Now().UTC()
			_, err = d.UpsertIssue(t.Context(), &db.Issue{RepoID: id, PlatformID: 7, Number: 7, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now})
			require.NoError(t, err)
			_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: id, PlatformID: 8, Number: 8, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now})
			require.NoError(t, err)
			provider := &closureFailureProvider{syncTestReadProvider: &syncTestReadProvider{kind: repo.Platform, host: repo.PlatformHost, getIssueErr: issueErr}, failure: mrErr}
			registry, err := platform.NewRegistry(provider)
			require.NoError(t, err)
			s := NewSyncerWithRegistry(registry, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			require.Positive(t, provider.getIssueCalls.Load())
			require.Positive(t, provider.getMRCalls.Load())
			if status == 404 {
				assert.Empty(t, events)
			} else {
				assert.Equal(t, [][2]string{{"merge_requests", "provider"}, {"issues", "provider"}}, events)
			}
		})
	}
}

func TestSyncFailedKeylessCredentialRoute(t *testing.T) {
	d := openTestDB(t)
	repo := RepoRef{Owner: "unrouted", Name: "widget", PlatformHost: "github.com"}
	_, err := reposeed.Seed(t.Context(), d, verifiedDBRepoIdentity(platformRepoRef(repo)))
	require.NoError(t, err)
	s := NewSyncer(map[string]Client{"github.com": &mockClient{}}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	router, err := NewHostRouter("github.com", nil)
	require.NoError(t, err)
	s.SetGitHubRouters(map[string]*HostRouter{"github.com": router})
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
}
