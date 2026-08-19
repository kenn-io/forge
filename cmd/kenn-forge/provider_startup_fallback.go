package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v89/github"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/githubapp"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/tokenauth"
)

type hostAccountingIdentityResolver struct{}

func (hostAccountingIdentityResolver) ResolvePAT(
	ctx context.Context,
	host string,
	source tokenauth.Source,
) (github.GitHubIdentity, string, error) {
	token, err := source.Token(tokenauth.WithMutationAuth(ctx))
	if err != nil {
		return github.GitHubIdentity{}, "", err
	}
	return github.GitHubIdentity{Key: github.HostIdentity(host)}, token, nil
}

type fallbackIdentityResolver struct {
	primary               github.IdentityResolver
	degradedProviderHosts map[string]struct{}
}

func (r fallbackIdentityResolver) ResolvePAT(
	ctx context.Context,
	host string,
	source tokenauth.Source,
) (github.GitHubIdentity, string, error) {
	identity, token, err := r.primary.ResolvePAT(ctx, host, source)
	if err == nil || !isTransientGitHubStartupError(err) {
		return identity, token, err
	}
	r.degradedProviderHosts[providerHostKey(string(platform.KindGitHub), host)] = struct{}{}
	return (hostAccountingIdentityResolver{}).ResolvePAT(ctx, host, source)
}

func buildProviderStartupWithFallback(
	ctx context.Context,
	database *db.DB,
	cfg *config.Config,
	set *tokenauth.SourceSet,
	factories map[string]providerFactory,
	resolver github.IdentityResolver,
) (providerStartup, error) {
	providerSources, degradedProviderHosts, err := collectProviderTokenSourcesWithFallback(
		ctx, cfg, set,
	)
	if err != nil {
		return providerStartup{}, err
	}
	startup, err := buildProviderStartup(
		ctx, database, cfg, set, providerSources, factories, fallbackIdentityResolver{
			primary:               resolver,
			degradedProviderHosts: degradedProviderHosts,
		},
	)
	if err != nil {
		return providerStartup{}, err
	}
	startup.degradedProviderHosts = degradedProviderHosts
	if len(degradedProviderHosts) > 0 {
		slog.Warn(
			"GitHub unavailable during provider startup; serving local archive while sync retries",
			"degraded_hosts", len(degradedProviderHosts),
		)
	}
	return startup, nil
}

func isTransientGitHubStartupError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return true
	}
	var rateLimitErr *gh.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return true
	}
	var abuseRateLimitErr *gh.AbuseRateLimitError
	if errors.As(err, &abuseRateLimitErr) {
		return true
	}
	var responseErr *gh.ErrorResponse
	if errors.As(err, &responseErr) && responseErr.Response != nil {
		status := responseErr.Response.StatusCode
		return status == http.StatusRequestTimeout ||
			status == http.StatusTooManyRequests || status >= 500
	}
	var appErr *githubapp.StatusError
	if errors.As(err, &appErr) {
		if appErr.StatusCode == http.StatusForbidden {
			return appErr.Header.Get("X-RateLimit-Remaining") == "0" ||
				appErr.Header.Get("Retry-After") != "" ||
				isGitHubSecondaryRateLimitBody(appErr.Body)
		}
		return appErr.StatusCode == http.StatusRequestTimeout ||
			appErr.StatusCode == http.StatusTooManyRequests ||
			appErr.StatusCode >= 500
	}
	return false
}

func isGitHubSecondaryRateLimitBody(body string) bool {
	var response struct {
		DocumentationURL string `json:"documentation_url"`
	}
	if json.Unmarshal([]byte(body), &response) != nil {
		return false
	}
	documentationURL := strings.TrimSpace(response.DocumentationURL)
	return strings.HasSuffix(documentationURL, "secondary-rate-limits") ||
		strings.HasSuffix(documentationURL, "#abuse-rate-limits")
}
