package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	forgejoclient "go.kenn.io/middleman/internal/platform/forgejo"
	giteaclient "go.kenn.io/middleman/internal/platform/gitea"
	gitlabclient "go.kenn.io/middleman/internal/platform/gitlab"
	"go.kenn.io/middleman/internal/tokenauth"
)

type providerFactory func(providerFactoryInput) (providerFactoryOutput, error)

type providerFactoryInput struct {
	host        string
	tokenSource tokenauth.Source
	rateTracker *github.RateTracker
	budget      *github.SyncBudget
}

type providerFactoryOutput struct {
	githubClient github.Client
	provider     platform.Provider
}

type graphQLRateTrackerSetter interface {
	SetGraphQLRateTracker(*github.RateTracker)
}

type writeRateTrackerSetter interface {
	SetWriteRateTracker(*github.RateTracker)
	SetWriteGraphQLRateTracker(*github.RateTracker)
}

type providerStartup struct {
	registry             *platform.Registry
	rateTrackers         map[string]*github.RateTracker
	writeRateTrackers    map[string]*github.RateTracker
	writeGQLRateTrackers map[string]*github.RateTracker
	budgets              map[string]*github.SyncBudget
	cloneSources         map[tokenauth.Key]tokenauth.Source
	cloneAuth            map[string]tokenauth.Source
	fetchers             map[string]*github.GraphQLFetcher
}

func defaultProviderFactories() map[string]providerFactory {
	return map[string]providerFactory{
		string(platform.KindGitHub): func(input providerFactoryInput) (providerFactoryOutput, error) {
			client, err := github.NewClient(
				input.tokenSource, input.host, input.rateTracker, input.budget,
			)
			if err != nil {
				return providerFactoryOutput{}, err
			}
			return providerFactoryOutput{
				githubClient: client,
			}, nil
		},
		string(platform.KindGitLab): func(input providerFactoryInput) (providerFactoryOutput, error) {
			client, err := gitlabclient.NewClient(
				input.host, input.tokenSource,
				gitlabclient.WithRateTracker(input.rateTracker),
			)
			if err != nil {
				return providerFactoryOutput{}, err
			}
			return providerFactoryOutput{provider: client}, nil
		},
		string(platform.KindForgejo): func(input providerFactoryInput) (providerFactoryOutput, error) {
			client, err := forgejoclient.NewClient(
				input.host, input.tokenSource,
				forgejoclient.WithRateTracker(input.rateTracker),
				forgejoclient.WithSyncBudget(input.budget),
			)
			if err != nil {
				return providerFactoryOutput{}, err
			}
			return providerFactoryOutput{provider: client}, nil
		},
		string(platform.KindGitea): func(input providerFactoryInput) (providerFactoryOutput, error) {
			client, err := giteaclient.NewClient(
				input.host, input.tokenSource,
				giteaclient.WithRateTracker(input.rateTracker),
				giteaclient.WithSyncBudget(input.budget),
			)
			if err != nil {
				return providerFactoryOutput{}, err
			}
			return providerFactoryOutput{provider: client}, nil
		},
	}
}

func collectProviderTokenSources(
	ctx context.Context,
	cfg *config.Config,
	set *tokenauth.SourceSet,
) (map[string]tokenauth.Source, error) {
	providerSources := make(map[string]tokenauth.Source, len(cfg.Repos)+len(cfg.Platforms)+1)
	add := func(plan config.ProviderTokenSource) error {
		desc := plan.Descriptor
		key := providerHostKey(desc.Key.Platform, desc.Key.Host)
		src, seen := providerSources[key]
		if !seen {
			src = set.Upsert(desc)
		}
		tokenCtx := ctx
		if plan.GitHubOwner != "" {
			tokenCtx = tokenauth.WithGitHubOwner(tokenCtx, plan.GitHubOwner)
		}
		if _, err := src.Token(tokenCtx); err != nil {
			if !plan.Required && errors.Is(err, tokenauth.ErrMissingToken) {
				return nil
			}
			label := fmt.Sprintf("%s host %s", desc.Key.Platform, desc.Key.Host)
			if plan.GitHubOwner != "" {
				label = fmt.Sprintf("%s owner %s", label, plan.GitHubOwner)
			}
			if plan.Required {
				return fmt.Errorf("no token for %s via %s: %w", label, desc.SafeString(), err)
			}
			return fmt.Errorf(
				"read optional token for %s via %s: %w",
				label, desc.SafeString(), err,
			)
		}
		if !seen {
			providerSources[key] = src
		}
		return nil
	}
	for _, plan := range cfg.ProviderTokenSources() {
		if err := add(plan); err != nil {
			return nil, err
		}
	}
	if err := validateProviderHostKeys(providerSources); err != nil {
		return nil, err
	}
	return providerSources, nil
}

func buildProviderStartup(
	database *db.DB,
	cfg *config.Config,
	set *tokenauth.SourceSet,
	providerSources map[string]tokenauth.Source,
	factories map[string]providerFactory,
) (providerStartup, error) {
	if err := validateProviderHostKeys(providerSources); err != nil {
		return providerStartup{}, err
	}
	startup := providerStartup{
		rateTrackers:         make(map[string]*github.RateTracker, len(providerSources)),
		writeRateTrackers:    make(map[string]*github.RateTracker, len(providerSources)),
		writeGQLRateTrackers: make(map[string]*github.RateTracker, len(providerSources)),
		budgets:              make(map[string]*github.SyncBudget, len(providerSources)),
		cloneSources:         make(map[tokenauth.Key]tokenauth.Source, len(providerSources)),
		cloneAuth:            make(map[string]tokenauth.Source, len(providerSources)),
		fetchers:             make(map[string]*github.GraphQLFetcher, len(providerSources)),
	}
	budgetPerHour := cfg.BudgetPerHour()
	clients := make(map[string]github.Client, len(providerSources))
	providers := make([]platform.Provider, 0, len(providerSources))
	githubHosts := make(map[string]struct{}, len(providerSources))
	for key, tokenSource := range providerSources {
		platformName, host := splitProviderHostKey(key)
		rateKey := github.RateBucketKey(platformName, host)
		if _, ok := startup.rateTrackers[rateKey]; !ok {
			startup.rateTrackers[rateKey] = github.NewPlatformRateTracker(
				database, platformName, host, "rest",
			)
		}
		if budgetPerHour > 0 {
			if _, ok := startup.budgets[rateKey]; !ok {
				startup.budgets[rateKey] = github.NewSyncBudget(budgetPerHour)
			}
		}
		factory, ok := factories[platformName]
		if !ok {
			return providerStartup{}, fmt.Errorf("unsupported platform %q", platformName)
		}
		built, err := factory(providerFactoryInput{
			host:        host,
			tokenSource: tokenSource,
			rateTracker: startup.rateTrackers[rateKey],
			budget:      startup.budgets[rateKey],
		})
		if err != nil {
			return providerStartup{}, fmt.Errorf(
				"create %s client for %s: %w", platformLabel(platformName), host, err,
			)
		}
		if built.githubClient != nil {
			clients[host] = built.githubClient
			githubHosts[host] = struct{}{}
		}
		if built.provider != nil {
			providers = append(providers, built.provider)
		}
		startup.cloneSources[tokenauth.Key{Platform: platformName, Host: host}] = tokenSource
	}
	// Clone auth is host-scoped: every provider sharing a host presents the
	// same canonical credential chain (validated above), so each host gets a
	// dedicated source keyed by tokenauth.CloneKey rather than borrowing
	// whichever provider source map iteration yielded first. Registering it
	// in the shared SourceSet lets config reload re-point clone/fetch at the
	// host's current effective chain (config.CloneTokenDescriptors) even when
	// the provider entry that supplied the credential changes. Hosts with no
	// resolved provider source keep no entry, so git runs unauthenticated
	// there — same as a credential-less host at startup today.
	for _, key := range slices.Sorted(maps.Keys(providerSources)) {
		_, host := splitProviderHostKey(key)
		if _, ok := startup.cloneAuth[host]; ok {
			continue
		}
		source := providerSources[key]
		if source == nil {
			continue
		}
		desc := source.Descriptor()
		desc.Key = tokenauth.CloneKey(host)
		startup.cloneAuth[host] = set.Upsert(desc)
	}
	registry, err := github.NewProviderRegistry(clients, providers...)
	if err != nil {
		return providerStartup{}, fmt.Errorf("create provider registry: %w", err)
	}
	startup.registry = registry
	for host := range githubHosts {
		rateKey := github.RateBucketKey(string(platform.KindGitHub), host)
		gqlRT := github.NewPlatformRateTracker(database, string(platform.KindGitHub), host, "graphql")
		if setter, ok := clients[host].(graphQLRateTrackerSetter); ok {
			setter.SetGraphQLRateTracker(gqlRT)
		}
		// Hosts whose sync reads ride a GitHub App split off the user's
		// PAT for writes; only those get dedicated write trackers, so
		// write availability gates on the budget writes actually
		// consume. The split is read from the host's effective
		// credential chain, not from [[github_apps]] config alone: a
		// host whose repos all carry terminal token overrides never
		// uses the app candidate, and an empty write tracker would
		// shadow the shared trackers exhausted sync had observed.
		hostSource := startup.cloneSources[tokenauth.Key{
			Platform: string(platform.KindGitHub),
			Host:     host,
		}]
		if hostSource != nil && hostSource.Descriptor().HasActiveGitHubApp() {
			if setter, ok := clients[host].(writeRateTrackerSetter); ok {
				writeRT := github.NewPlatformRateTracker(
					database, string(platform.KindGitHub), host, "rest_write",
				)
				writeGQLRT := github.NewPlatformRateTracker(
					database, string(platform.KindGitHub), host, "graphql_write",
				)
				setter.SetWriteRateTracker(writeRT)
				setter.SetWriteGraphQLRateTracker(writeGQLRT)
				startup.writeRateTrackers[rateKey] = writeRT
				startup.writeGQLRateTrackers[rateKey] = writeGQLRT
			}
		}
		source := startup.cloneSources[tokenauth.Key{
			Platform: string(platform.KindGitHub),
			Host:     host,
		}]
		startup.fetchers[host] = github.NewGraphQLFetcher(
			source, host, gqlRT, startup.budgets[rateKey],
		)
	}
	return startup, nil
}

func platformLabel(platformName string) string {
	if meta, ok := platform.MetadataFor(platform.Kind(platformName)); ok {
		return meta.Label
	}
	return platformName
}
