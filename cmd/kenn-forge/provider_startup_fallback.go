package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	gh "github.com/google/go-github/v89/github"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/githubapp"
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

func buildProviderStartupWithFallback(
	ctx context.Context,
	database *db.DB,
	cfg *config.Config,
	set *tokenauth.SourceSet,
	factories map[string]providerFactory,
	resolver github.IdentityResolver,
) (providerStartup, error) {
	providerSources, err := collectProviderTokenSources(ctx, cfg, set)
	if err == nil {
		var startup providerStartup
		startup, err = buildProviderStartup(
			ctx, database, cfg, set, providerSources, factories, resolver,
		)
		if err == nil {
			return startup, nil
		}
	}
	if !isTransientGitHubStartupError(err) {
		return providerStartup{}, err
	}

	slog.Warn(
		"GitHub unavailable during provider startup; serving local archive while sync retries",
		"error", err,
	)
	providerSources, fallbackErr := registerProviderTokenSources(cfg, set)
	if fallbackErr != nil {
		return providerStartup{}, fmt.Errorf(
			"register degraded provider sources: %w", fallbackErr,
		)
	}
	startup, fallbackErr := buildProviderStartup(
		ctx, database, cfg, set, providerSources, factories,
		hostAccountingIdentityResolver{},
	)
	if fallbackErr != nil {
		return providerStartup{}, fmt.Errorf(
			"build degraded provider startup: %w", fallbackErr,
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
		return appErr.StatusCode == http.StatusRequestTimeout ||
			appErr.StatusCode == http.StatusTooManyRequests ||
			appErr.StatusCode >= 500
	}
	return false
}
