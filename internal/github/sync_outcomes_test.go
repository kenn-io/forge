package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/activityrelay"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucket"
	"go.kenn.io/forge/platform/bitbucketdc"
	"go.kenn.io/forge/platform/forgejo"
	"go.kenn.io/forge/platform/gitea"
)

func TestSyncFailureReason(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{nil, ""},
		{tokenauth.ErrMissingToken, "auth"},
		{&platform.Error{Code: platform.ErrCodeRateLimited, Err: &gh.ErrorResponse{Response: &http.Response{StatusCode: 403}}}, "rate_limited"},
		{&gh.RateLimitError{}, "rate_limited"},
		{&gh.AbuseRateLimitError{}, "rate_limited"},
		{&gh.ErrorResponse{Response: &http.Response{StatusCode: 401}}, "auth"},
		{&gh.ErrorResponse{Response: &http.Response{StatusCode: 404}}, "not_found"},
		{&gh.ErrorResponse{Response: &http.Response{StatusCode: 502}}, "provider"},
		{&gh.ErrorResponse{Response: &http.Response{StatusCode: 418}}, "other"},
		{platform.ErrStaleState, "other"},
		{platform.ErrProviderContract, "provider"},
		{errors.New("private detail"), "other"},
	} {
		t.Run(tc.reason+fmt.Sprint(tc.err), func(t *testing.T) {
			err := tc.err
			if err != nil {
				err = fmt.Errorf("sync: %w", err)
			}
			assert.Equal(t, tc.reason, syncFailureReason(err))
		})
	}
}

func TestRepositoryFailuresCountEveryAttempt(t *testing.T) {
	client := &mockClient{listOpenPRsErr: platform.ErrPermissionDenied}
	syncer := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil,
		[]RepoRef{{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}, {Owner: "acme", Name: "tools", PlatformHost: "github.com"}}, time.Minute, nil, nil)
	syncer.SetParallelism(1)
	var reasons []string
	var results []RepoSyncResult
	syncer.SetOnSyncCompleted(func(batch []RepoSyncResult) {
		results = batch
		for _, result := range batch {
			if result.FailureReason != "" {
				reasons = append(reasons, result.FailureReason)
			}
		}
	})
	syncer.RunOnce(t.Context())
	require.Len(t, results, 2)
	for _, result := range results {
		assert.NotEmpty(t, result.Error)
	}
	assert.Equal(t, []string{"auth", "auth"}, reasons)
	syncer.RunOnce(t.Context())
	require.Error(t, syncer.SyncRepoOnProvider(t.Context(), platform.KindGitHub, "github.com", "acme", "widgets"))
	require.Error(t, syncer.SyncRepoOnProvider(t.Context(), platform.KindGitHub, "github.com", "acme", "untracked"))
	assert.Equal(t, []string{"auth", "auth", "auth", "auth", "auth"}, reasons)
}

func TestRelayRepositoryFailureOutcomes(t *testing.T) {
	for _, target := range []string{activityrelay.Repository, activityrelay.RepositoryRefs} {
		t.Run(target, func(t *testing.T) {
			database := openTestDB(t)
			repo := RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Key: platform.RepositoryIDKey(1002), Owner: "team", Name: "project"}
			_, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{
				Platform: "github", PlatformHost: "github.com", Key: repo.Key, Owner: repo.Owner, Name: repo.Name,
			})
			require.NoError(t, err)
			failed := true
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v3/repositories/1002" {
					_, _ = fmt.Fprint(w, `{"id":1002,"name":"project","owner":{"login":"team"},"default_branch":"main","has_issues":true}`)
				} else if failed {
					w.WriteHeader(http.StatusForbidden)
					_, _ = fmt.Fprint(w, `{"message":"Permission denied"}`)
				} else {
					_, _ = fmt.Fprint(w, `[]`)
				}
			}))
			t.Cleanup(provider.Close)
			budget := NewSyncBudget(1000)
			client, err := NewClient(testTokenSource("token"), "github.com", nil, budget, WithBaseURLForTesting(provider.URL))
			require.NoError(t, err)
			syncer := NewSyncer(map[string]Client{"github.com": client}, database, nil, []RepoRef{repo}, time.Minute, nil, map[string]*SyncBudget{"github.com": budget})
			var reasons []string
			syncer.SetOnSyncCompleted(func(batch []RepoSyncResult) {
				for _, result := range batch {
					if result.FailureReason != "" {
						reasons = append(reasons, result.FailureReason)
					}
				}
			})
			hint := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: 1002, Target: target}
			for range 2 {
				require.Error(t, syncer.refreshRelayHint(WithSyncBudget(t.Context()), hint))
			}
			assert.Equal(t, []string{"auth", "auth"}, reasons)
			budget.Reset()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_ = syncer.refreshRelayHint(WithSyncBudget(ctx), hint)
			failed = false
			require.NoError(t, syncer.refreshRelayHint(WithSyncBudget(t.Context()), hint))
			assert.Equal(t, []string{"auth", "auth"}, reasons)
			router, err := NewHostRouter("github.com", &Route{Key: RouteKey{Host: "github.com", Owner: "another-team"}, ReadIdentity: IdentityKey{Host: "github.com", Principal: "user:42"}})
			require.NoError(t, err)
			syncer.routers = map[string]*HostRouter{"github.com": router}
			require.Error(t, syncer.refreshRelayHint(WithSyncBudget(t.Context()), hint))
			assert.Equal(t, []string{"auth", "auth", "auth"}, reasons)
		})
	}
}

func TestRepositoryFailureBeforeDispatch(t *testing.T) {
	client := &mockClient{}
	repo := RepoRef{Owner: "team", Name: "project", PlatformHost: "github.com"}
	syncer := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	router, err := NewHostRouter("github.com", &Route{Key: RouteKey{Host: "github.com", Owner: "another-team"}, ReadIdentity: IdentityKey{Host: "github.com", Principal: "user:42"}})
	require.NoError(t, err)
	syncer.routers = map[string]*HostRouter{"github.com": router}
	var reasons []string
	syncer.SetOnSyncCompleted(func(batch []RepoSyncResult) {
		for _, result := range batch {
			if result.FailureReason != "" {
				reasons = append(reasons, result.FailureReason)
			}
		}
	})
	syncer.RunOnce(t.Context())
	assert.Equal(t, []string{"auth"}, reasons)
}

func TestNativeProviderServerFailuresReportProviderOutcome(t *testing.T) {
	source := testTokenSource("token")
	for _, tc := range []struct {
		name   string
		client func(http.RoundTripper) (platform.Provider, error)
	}{
		{"gitea", func(transport http.RoundTripper) (platform.Provider, error) {
			return gitea.NewClient("gitea.example.com", source, gitea.WithTransport(transport))
		}},
		{"forgejo", func(transport http.RoundTripper) (platform.Provider, error) {
			return forgejo.NewClient("forgejo.example.com", source, forgejo.WithTransport(transport))
		}},
		{"bitbucket-cloud", func(transport http.RoundTripper) (platform.Provider, error) {
			return bitbucket.NewClient("bitbucket.org", source, transport, nil)
		}},
		{"bitbucket-dc", func(transport http.RoundTripper) (platform.Provider, error) {
			return bitbucketdc.NewClient("bitbucket.example.com", source, transport, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			provider, err := tc.client(platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				reads++
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable",
					Header: http.Header{"Content-Type": {"application/json"}},
					Body:   io.NopCloser(strings.NewReader(`{"message":"temporarily unavailable"}`)), Request: r,
				}, nil
			}))
			require.NoError(t, err)
			registry, err := platform.NewRegistry(provider)
			require.NoError(t, err)
			repo := RepoRef{Platform: provider.Platform(), PlatformHost: provider.Host(), Owner: "team", Name: "project"}
			syncer := NewSyncerWithRegistry(registry, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
			var reasons []string
			syncer.SetOnSyncCompleted(func(batch []RepoSyncResult) {
				for _, result := range batch {
					if result.FailureReason != "" {
						reasons = append(reasons, result.FailureReason)
					}
				}
			})
			err = syncer.SyncRepoOnProvider(t.Context(), provider.Platform(), provider.Host(), repo.Owner, repo.Name)
			require.Error(t, err)
			require.NotErrorIs(t, err, platform.ErrProviderContract)
			assert.Positive(t, reads)
			assert.Equal(t, []string{"provider"}, reasons)
		})
	}
}

func TestRepositoryCanceledAndSkippedOutcomes(t *testing.T) {
	for _, failure := range []error{context.Canceled, platform.ErrSyncBudgetExhausted, platform.ErrArchiveAttemptBudget, platform.ErrSyncDisabled} {
		t.Run(failure.Error(), func(t *testing.T) {
			syncer := NewSyncer(map[string]Client{"github.com": &mockClient{listOpenPRsErr: failure}}, openTestDB(t), nil, []RepoRef{{Owner: "team", Name: "project", PlatformHost: "github.com"}}, time.Minute, nil, nil)
			var reasons []string
			syncer.SetOnSyncCompleted(func(results []RepoSyncResult) {
				for _, result := range results {
					if result.FailureReason != "" {
						reasons = append(reasons, result.FailureReason)
					}
				}
			})
			syncer.RunOnce(t.Context())
			_ = syncer.SyncRepoOnProvider(t.Context(), platform.KindGitHub, "github.com", "team", "project")
			assert.Empty(t, reasons)
		})
	}
}
