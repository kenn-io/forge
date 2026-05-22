package main

import (
	"fmt"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	forgejoclient "go.kenn.io/middleman/internal/platform/forgejo"
	giteaclient "go.kenn.io/middleman/internal/platform/gitea"
	gitlabclient "go.kenn.io/middleman/internal/platform/gitlab"
)

type providerFactory func(providerFactoryInput) (providerFactoryOutput, error)

type providerFactoryInput struct {
	host        string
	token       string
	rateTracker *github.RateTracker
	budget      *github.SyncBudget
}

type providerFactoryOutput struct {
	githubClient github.Client
	provider     platform.Provider
	githubToken  string
}

type providerStartup struct {
	registry     *platform.Registry
	rateTrackers map[string]*github.RateTracker
	budgets      map[string]*github.SyncBudget
	cloneTokens  map[string]string
	fetchers     map[string]*github.GraphQLFetcher
}

func defaultProviderFactories() map[string]providerFactory {
	return map[string]providerFactory{
		string(platform.KindGitHub): func(input providerFactoryInput) (providerFactoryOutput, error) {
			client, err := github.NewClient(
				input.token, input.host, input.rateTracker, input.budget,
			)
			if err != nil {
				return providerFactoryOutput{}, err
			}
			return providerFactoryOutput{
				githubClient: client,
				githubToken:  input.token,
			}, nil
		},
		string(platform.KindGitLab): func(input providerFactoryInput) (providerFactoryOutput, error) {
			client, err := gitlabclient.NewClient(
				input.host, input.token,
				gitlabclient.WithRateTracker(input.rateTracker),
			)
			if err != nil {
				return providerFactoryOutput{}, err
			}
			return providerFactoryOutput{provider: client}, nil
		},
		string(platform.KindForgejo): func(input providerFactoryInput) (providerFactoryOutput, error) {
			client, err := forgejoclient.NewClient(
				input.host, input.token,
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
				input.host, input.token,
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

func collectProviderTokens(cfg *config.Config) (map[string]string, error) {
	providerTokens := make(map[string]string, len(cfg.Repos)+len(cfg.Platforms)+1)
	for _, r := range cfg.Repos {
		platformName := r.PlatformOrDefault()
		host := r.PlatformHostOrDefault()
		key := providerHostKey(platformName, host)
		if _, seen := providerTokens[key]; seen {
			continue
		}
		token := cfg.ResolveRepoToken(r)
		if token == "" {
			return nil, fmt.Errorf(
				"no token for %s host %s (repo %s/%s)",
				platformName, host, r.Owner, r.Name,
			)
		}
		providerTokens[key] = token
	}
	for _, p := range cfg.Platforms {
		key := providerHostKey(p.Type, p.Host)
		if _, seen := providerTokens[key]; seen {
			continue
		}
		token := cfg.TokenForPlatformHost(p.Type, p.Host, "")
		if token != "" {
			providerTokens[key] = token
		}
	}
	globalGitHubToken := cfg.GitHubToken()
	defaultGitHubKey := providerHostKey(
		string(platform.KindGitHub), platform.DefaultGitHubHost,
	)
	if globalGitHubToken != "" {
		if _, ok := providerTokens[defaultGitHubKey]; !ok {
			providerTokens[defaultGitHubKey] = globalGitHubToken
		}
	}
	if err := validateProviderHostKeys(providerTokens); err != nil {
		return nil, err
	}
	return providerTokens, nil
}

func buildProviderStartup(
	database *db.DB,
	cfg *config.Config,
	providerTokens map[string]string,
	factories map[string]providerFactory,
) (providerStartup, error) {
	if err := validateProviderHostKeys(providerTokens); err != nil {
		return providerStartup{}, err
	}
	startup := providerStartup{
		rateTrackers: make(map[string]*github.RateTracker, len(providerTokens)),
		budgets:      make(map[string]*github.SyncBudget, len(providerTokens)),
		cloneTokens:  make(map[string]string, len(providerTokens)),
		fetchers:     make(map[string]*github.GraphQLFetcher, len(providerTokens)),
	}
	budgetPerHour := cfg.BudgetPerHour()
	clients := make(map[string]github.Client, len(providerTokens))
	providers := make([]platform.Provider, 0, len(providerTokens))
	githubTokens := make(map[string]string, len(providerTokens))
	for key, token := range providerTokens {
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
			token:       token,
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
		}
		if built.provider != nil {
			providers = append(providers, built.provider)
		}
		if built.githubToken != "" {
			githubTokens[host] = built.githubToken
		}
		if _, ok := startup.cloneTokens[host]; !ok {
			startup.cloneTokens[host] = token
		}
	}
	registry, err := github.NewProviderRegistry(clients, providers...)
	if err != nil {
		return providerStartup{}, fmt.Errorf("create provider registry: %w", err)
	}
	startup.registry = registry
	for host, token := range githubTokens {
		rateKey := github.RateBucketKey(string(platform.KindGitHub), host)
		gqlRT := github.NewPlatformRateTracker(database, string(platform.KindGitHub), host, "graphql")
		startup.fetchers[host] = github.NewGraphQLFetcher(
			token, host, gqlRT, startup.budgets[rateKey],
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
