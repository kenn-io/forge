package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
	appauth "go.kenn.io/forge/internal/githubapp"
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

func TestSyncFailedGitHubChecksIgnores404(t *testing.T) {
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
				s.finishSyncPass(pass)
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

func TestSyncFailedManagedTokenRenewal(t *testing.T) { //nolint:paralleltest // swaps http.DefaultTransport
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))
	for _, tc := range []struct {
		status     int
		retryAfter bool
		reason     string
	}{
		{http.StatusUnauthorized, false, "auth"},
		{http.StatusForbidden, false, "auth"},
		{http.StatusNotFound, false, "not_found"},
		{http.StatusTooManyRequests, true, "rate_limited"},
		{http.StatusForbidden, true, "rate_limited"},
	} {
		t.Run(fmt.Sprintf("status=%d/retry=%t", tc.status, tc.retryAfter), func(t *testing.T) {
			assert := assert.New(t)
			var mints atomic.Int32
			host := withGitHubAuthTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(http.MethodPost, r.Method)
				assert.Equal("/api/v3/app/installations/11/access_tokens", r.URL.Path)
				if mints.Add(1) == 1 {
					w.WriteHeader(http.StatusCreated)
					fmt.Fprintf(w, `{"token":"installation-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
					return
				}
				if tc.retryAfter {
					w.Header().Set("Retry-After", "90")
				}
				http.Error(w, "mint failed", tc.status)
			}))
			source := tokenauth.NewManagedSource(tokenauth.Descriptor{
				Key:        tokenauth.Key{Platform: "github", Host: host},
				Candidates: []tokenauth.Candidate{{Kind: tokenauth.SourceKindGitHubApp, Host: host, AppID: 7, FilePath: keyPath, InstallationID: 11}},
			}, tokenauth.Options{GitHubApp: func(ctx context.Context, candidate tokenauth.Candidate) (string, time.Time, error) {
				return appauth.MintInstallationToken(ctx, candidate.Host, candidate.AppID, candidate.FilePath, candidate.InstallationID)
			}})
			token, err := source.Token(t.Context())
			require.NoError(t, err)
			source.Invalidate(token)
			client, err := NewClient(source, host, nil, nil)
			require.NoError(t, err)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: host}
			s := NewSyncer(map[string]Client{host: client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			assert.Equal([][2]string{{"merge_requests", tc.reason}, {"issues", tc.reason}}, events)
			_, err = source.Token(t.Context())
			var status *githubapp.StatusError
			require.ErrorAs(t, err, &status)
			assert.Equal(tc.status, status.StatusCode)
			assert.Equal(int32(2), mints.Load())
			if tc.retryAfter {
				var retry interface{ RetryDeadline(time.Time) time.Time }
				require.ErrorAs(t, err, &retry)
				now := time.Now()
				assert.Equal(now.Add(90*time.Second), retry.RetryDeadline(now))
			}
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
	pass := &syncPass{}
	pass.observe(repo, "checks", platform.ErrPermissionDenied)
	ctx := context.WithValue(t.Context(), syncPassKey{}, pass)
	require.NoError(t, s.syncOpenMRFromBulk(ctx, repo, id, time.Time{}, &BulkPR{
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
