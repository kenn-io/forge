package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

// scriptedIdentityResolver fails its first calls with errs, in order, and
// resolves the configured token's identity after that.
type scriptedIdentityResolver struct {
	errs  []error
	calls *atomic.Int32
}

func (r scriptedIdentityResolver) ResolvePAT(
	ctx context.Context, host string, source tokenauth.Source,
) (github.GitHubIdentity, string, error) {
	call := int(r.calls.Add(1)) - 1
	if call < len(r.errs) {
		return github.GitHubIdentity{}, "", r.errs[call]
	}
	token, err := source.Token(tokenauth.WithMutationAuth(ctx))
	if err != nil {
		return github.GitHubIdentity{}, "", err
	}
	return github.GitHubIdentity{Key: github.IdentityKey{Host: host, Principal: "user:7"}}, token, nil
}

func identityLookupResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Request: &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Scheme: "https", Host: "api.github.com", Path: "/user"},
		},
	}
}

type recoveryHarness struct {
	syncer   *github.Syncer
	recovery *providerHostRecovery
	waits    []time.Time
	started  []string
	failures map[string]error
}

// startDegraded boots the provider plane the way serve does, with one GitHub
// host whose identity lookups follow resolver.
func startDegraded(t *testing.T, resolver github.IdentityResolver, now time.Time) *recoveryHarness {
	t.Helper()
	t.Setenv("ORG_A_TOKEN", "synthetic-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos:    []config.Repo{{Owner: "org-a", Name: "one"}},
		GitHubOwnerTokens: []config.GitHubOwnerTokenConfig{{
			Host: "github.com", Owner: "org-a", TokenEnv: "ORG_A_TOKEN",
		}},
	}
	require.NoError(t, cfg.Validate())
	database := dbtest.Open(t)
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	planes, err := buildServeControlPlanes(
		t.Context(), database, cfg, set, defaultProviderFactories(), resolver, false,
	)
	require.NoError(t, err)
	control := planes.Provider
	h := &recoveryHarness{failures: planes.HostFailures}
	h.syncer = github.NewSyncerWithRegistry(
		control.registry, database, nil, nil, time.Minute,
		control.rateTrackers, control.budgets,
	)
	h.recovery = &providerHostRecovery{
		database: database, startupCfg: *cfg, withConfig: currentConfig(cfg), set: set,
		factories: defaultProviderFactories(), resolver: resolver,
		syncer: h.syncer, quota: control.quotaRegistry, git: planes.GitRoutes,
		now: func() time.Time { return now },
		wait: func(_ context.Context, at time.Time) bool {
			h.waits = append(h.waits, at)
			return true
		},
		onStarted: func(_ context.Context, kind platform.Kind, host string) {
			h.started = append(h.started, providerHostKey(string(kind), host))
		},
	}
	return h
}

func TestRateLimitedGitHubHostStartsAfterReset(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	reset := now.Add(4 * time.Minute)
	calls := &atomic.Int32{}
	h := startDegraded(t, scriptedIdentityResolver{calls: calls, errs: []error{
		&gh.RateLimitError{
			Rate:     gh.Rate{Reset: gh.Timestamp{Time: reset}},
			Response: identityLookupResponse(http.StatusForbidden),
			Message:  "API rate limit exceeded",
		},
	}}, now)
	key := providerHostKey("github", "github.com")
	require.Contains(h.failures, key)

	loop := h.recovery.begin(key, h.failures[key])
	require.NotNil(loop, "a rate limit clears on its own, so the host is retried")
	outage, ok := h.syncer.ProviderHostOutage(platform.KindGitHub, "github.com")
	require.True(ok)
	assert.Equal(github.ProviderHostOutage{Reason: "rate limit exceeded", RetryAt: reset}, outage)
	_, err := h.syncer.ProviderCapabilities(platform.KindGitHub, "github.com")
	require.ErrorIs(err, platform.ErrProviderNotConfigured)

	loop(t.Context())

	assert.Equal([]time.Time{reset}, h.waits, "the retry waits for the provider's reset time")
	caps, err := h.syncer.ProviderCapabilities(platform.KindGitHub, "github.com")
	require.NoError(err)
	assert.True(caps.MergeMutation)
	_, err = h.syncer.SyncRegistry().MergeRequestReader(platform.KindGitHub, "github.com")
	require.NoError(err, "sync reads see the started host")
	assert.NotNil(h.syncer.GitHubRouters()["github.com"])
	_, ok = h.syncer.ProviderHostOutage(platform.KindGitHub, "github.com")
	assert.False(ok)
	assert.Equal([]string{key}, h.started)
}

func TestUnreachableGitHubHostRetriesWithGrowingWaits(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	unreachable := &url.Error{Op: "Get", URL: "https://api.github.com/user", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: errors.New("connection refused"),
	}}
	calls := &atomic.Int32{}
	h := startDegraded(t, scriptedIdentityResolver{calls: calls, errs: []error{
		unreachable, unreachable,
	}}, now)
	key := providerHostKey("github", "github.com")

	loop := h.recovery.begin(key, h.failures[key])
	require.NotNil(loop)
	loop(t.Context())

	assert.Equal([]time.Time{now.Add(30 * time.Second), now.Add(time.Minute)}, h.waits)
	_, err := h.syncer.ProviderCapabilities(platform.KindGitHub, "github.com")
	require.NoError(err)
}

func TestGiteaHostStartsAfterServerErrorClears(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	versionCalls := &atomic.Int32{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/version" {
			http.NotFound(w, r)
			return
		}
		if versionCalls.Add(1) == 1 {
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.26.0"}`))
	}))
	t.Cleanup(fake.Close)
	t.Setenv("GITEA_TOKEN", "synthetic-token")
	cfg := &config.Config{
		SyncInterval: "5m", Host: "127.0.0.1", Port: 8091, BasePath: "/",
		Activity: config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Platforms: []config.PlatformConfig{{
			Type: "gitea", Host: "gitea.test", BaseURL: fake.URL,
			AllowInsecure: true, TokenEnv: "GITEA_TOKEN",
		}},
		Repos: []config.Repo{{
			Owner: "team", Name: "service", Platform: "gitea", PlatformHost: "gitea.test",
		}},
	}
	require.NoError(cfg.Validate())
	database := dbtest.Open(t)
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	planes, err := buildServeControlPlanes(
		t.Context(), database, cfg, set, defaultProviderFactories(), nil, false,
	)
	require.NoError(err)
	key := providerHostKey("gitea", "gitea.test")
	require.Contains(planes.HostFailures, key)
	syncer := github.NewSyncerWithRegistry(
		planes.Provider.registry, database, nil, nil, time.Minute,
		planes.Provider.rateTrackers, planes.Provider.budgets,
	)
	now := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	var waits []time.Time
	recovery := &providerHostRecovery{
		database: database, startupCfg: *cfg, withConfig: currentConfig(cfg), set: set, factories: defaultProviderFactories(),
		syncer: syncer, quota: planes.Provider.quotaRegistry, git: planes.GitRoutes,
		now: func() time.Time { return now },
		wait: func(_ context.Context, at time.Time) bool {
			waits = append(waits, at)
			return true
		},
		onStarted: func(context.Context, platform.Kind, string) {},
	}

	loop := recovery.begin(key, planes.HostFailures[key])
	require.NotNil(loop)
	outage, ok := syncer.ProviderHostOutage(platform.KindGitea, "gitea.test")
	require.True(ok)
	assert.Equal("the provider returned a server error", outage.Reason)
	loop(t.Context())

	assert.Equal([]time.Time{now.Add(30 * time.Second)}, waits)
	_, err = syncer.SyncRegistry().RepositoryReader(platform.KindGitea, "gitea.test")
	require.NoError(err)
	_, ok = syncer.ProviderHostOutage(platform.KindGitea, "gitea.test")
	assert.False(ok)
}

func TestRecoveredHostUsesStartupCredentialsAndCurrentCeiling(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	calls := &atomic.Int32{}
	h := startDegraded(t, scriptedIdentityResolver{calls: calls, errs: []error{
		&gh.RateLimitError{
			Rate:     gh.Rate{Reset: gh.Timestamp{Time: now.Add(time.Minute)}},
			Response: identityLookupResponse(http.StatusForbidden),
			Message:  "API rate limit exceeded",
		},
	}}, now)
	// While the host was down, the config moved its token to an unset
	// variable, which a reload keeps from taking effect until restart, and
	// lowered the hourly ceiling, which applies at once.
	current := h.recovery.startupCfg
	current.GitHubOwnerTokens = []config.GitHubOwnerTokenConfig{{
		Host: "github.com", Owner: "org-a", TokenEnv: "ORG_A_UNSET_TOKEN",
	}}
	current.SyncBudgetPerHour = 1200
	h.recovery.withConfig = currentConfig(&current)
	key := providerHostKey("github", "github.com")

	loop := h.recovery.begin(key, h.failures[key])
	require.NotNil(loop)
	loop(t.Context())

	_, err := h.syncer.ProviderCapabilities(platform.KindGitHub, "github.com")
	require.NoError(err, "the host starts with the credential the daemon started with")
	require.NotEmpty(h.syncer.Budgets())
	for rateKey, budget := range h.syncer.Budgets() {
		assert.Equal(1200, budget.Limit(), rateKey)
	}
}

func TestRejectedGitHubCredentialStaysUnavailable(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	calls := &atomic.Int32{}
	h := startDegraded(t, scriptedIdentityResolver{calls: calls, errs: []error{
		&gh.ErrorResponse{
			Response: identityLookupResponse(http.StatusUnauthorized),
			Message:  "Bad credentials",
		},
	}}, now)
	key := providerHostKey("github", "github.com")

	loop := h.recovery.begin(key, h.failures[key])

	assert.Nil(loop, "a rejected credential needs a configuration change")
	outage, ok := h.syncer.ProviderHostOutage(platform.KindGitHub, "github.com")
	require.True(ok)
	assert.Equal(github.ProviderHostOutage{Reason: "the provider rejected the credential"}, outage)
	assert.Equal(int32(1), calls.Load())
}

func TestClassifyProviderStartFailure(t *testing.T) {
	now := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		err  error
		want providerStartFailure
	}{{
		name: "App token mint rate limited",
		err: &githubapp.StatusError{StatusCode: http.StatusForbidden, Header: http.Header{
			"X-Ratelimit-Remaining": {"0"},
			"X-Ratelimit-Reset":     {"1772618700"},
		}},
		want: providerStartFailure{
			transient: true, reason: "rate limit exceeded",
			resetAt: time.Unix(1772618700, 0).UTC(),
		},
	}, {
		name: "provider server error",
		err:  &githubapp.StatusError{StatusCode: http.StatusBadGateway},
		want: providerStartFailure{transient: true, reason: "the provider returned a server error"},
	}, {
		name: "secondary rate limit",
		err: &gh.AbuseRateLimitError{
			Response:   identityLookupResponse(http.StatusForbidden),
			RetryAfter: new(90 * time.Second),
		},
		want: providerStartFailure{
			transient: true, reason: "rate limit exceeded", resetAt: now.Add(90 * time.Second),
		},
	}, {
		name: "DNS failure",
		err:  &url.Error{Op: "Get", URL: "https://forge.example.test", Err: &net.DNSError{Err: "no such host", Name: "forge.example.test"}},
		want: providerStartFailure{transient: true, reason: "the provider could not be reached"},
	}, {
		name: "connection closed before a response",
		err:  &url.Error{Op: "Get", URL: "https://forge.example.test", Err: io.EOF},
		want: providerStartFailure{transient: true, reason: "the provider could not be reached"},
	}, {
		name: "missing credential",
		err:  tokenauth.ErrMissingToken,
		want: providerStartFailure{reason: "no credential is available"},
	}, {
		name: "unclassified",
		err:  errors.New("unexpected response"),
		want: providerStartFailure{reason: "it failed to start"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyProviderStartFailure(tc.err, now))
		})
	}
}

func TestUpstreamClosingConnectionIsRetried(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if !assert.NoError(t, err) {
			return
		}
		_ = conn.Close() // closing before any response is the scenario under test
	}))
	defer upstream.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)
	response, err := upstream.Client().Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	require.Error(t, err)
	failure := classifyProviderStartFailure(err, time.Now())
	assert.True(t, failure.transient, "an upstream dropping the connection can clear on its own: %v", err)
}

// currentConfig stands in for the server's reload-locked configuration.
func currentConfig(cfg *config.Config) func(context.Context, func(*config.Config) error) error {
	return func(_ context.Context, fn func(*config.Config) error) error { return fn(cfg) }
}
