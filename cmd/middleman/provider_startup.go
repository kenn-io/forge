package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

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

type githubIdentityRuntime struct {
	identity github.GitHubIdentity
	budget   *github.SyncBudget
	rest     *github.RateTracker
	graphql  *github.RateTracker
}

type githubCredentialRoute struct {
	key           tokenauth.Key
	source        tokenauth.Source
	client        github.Client
	fetcher       *github.GraphQLFetcher
	readIdentity  github.IdentityKey
	writeIdentity github.IdentityKey
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
	githubRoutes         map[tokenauth.Key]githubCredentialRoute
	githubIdentities     map[string]*githubIdentityRuntime
	githubRouters        map[string]*github.HostRouter
	githubClients        map[string]github.Client
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
				gitlabclient.WithSyncBudget(input.budget),
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
	ctx context.Context,
	database *db.DB,
	cfg *config.Config,
	set *tokenauth.SourceSet,
	providerSources map[string]tokenauth.Source,
	factories map[string]providerFactory,
	resolver github.IdentityResolver,
) (providerStartup, error) {
	if err := validateProviderHostKeys(providerSources); err != nil {
		return providerStartup{}, err
	}
	budgetPerHour := cfg.BudgetPerHour()
	startup := providerStartup{
		rateTrackers:         make(map[string]*github.RateTracker, len(providerSources)),
		writeRateTrackers:    make(map[string]*github.RateTracker, len(providerSources)),
		writeGQLRateTrackers: make(map[string]*github.RateTracker, len(providerSources)),
		budgets:              make(map[string]*github.SyncBudget, len(providerSources)),
		cloneSources:         make(map[tokenauth.Key]tokenauth.Source, len(providerSources)),
		cloneAuth:            make(map[string]tokenauth.Source, len(providerSources)),
		fetchers:             make(map[string]*github.GraphQLFetcher, len(providerSources)),
		githubRoutes:         make(map[tokenauth.Key]githubCredentialRoute),
		githubIdentities:     make(map[string]*githubIdentityRuntime),
		githubRouters:        make(map[string]*github.HostRouter),
		githubClients:        make(map[string]github.Client),
	}
	if resolver != nil {
		if err := buildGitHubIdentityRuntimes(
			ctx, database, cfg, set, resolver, budgetPerHour, &startup,
		); err != nil {
			return providerStartup{}, err
		}
		if err := buildGitHubRouteClients(&startup); err != nil {
			return providerStartup{}, err
		}
	}
	clients := make(map[string]github.Client, len(providerSources))
	providers := make([]platform.Provider, 0, len(providerSources))
	githubHosts := make(map[string]struct{}, len(providerSources))
	for key, tokenSource := range providerSources {
		platformName, host := splitProviderHostKey(key)
		rateKey := github.RateBucketKey(platformName, host, "host")
		if _, ok := startup.rateTrackers[rateKey]; !ok {
			startup.rateTrackers[rateKey] = github.NewPlatformRateTracker(
				database, platformName, host, "host", "rest",
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
			if routed := startup.githubClients[host]; routed != nil {
				clients[host] = routed
			} else {
				clients[host] = built.githubClient
			}
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
		rateKey := github.RateBucketKey(string(platform.KindGitHub), host, "host")
		gqlRT := github.NewPlatformRateTracker(database, string(platform.KindGitHub), host, "host", "graphql")
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
					database, string(platform.KindGitHub), host, "host", "rest_write",
				)
				writeGQLRT := github.NewPlatformRateTracker(
					database, string(platform.KindGitHub), host, "host", "graphql_write",
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

func buildGitHubIdentityRuntimes(
	ctx context.Context,
	database *db.DB,
	cfg *config.Config,
	set *tokenauth.SourceSet,
	resolver github.IdentityResolver,
	budgetPerHour int,
	startup *providerStartup,
) error {
	if cfg == nil || set == nil || resolver == nil {
		return nil
	}
	plans := githubCredentialPlans(cfg)
	resolvedPATs := make(map[string]github.GitHubIdentity, len(plans))
	for _, plan := range plans {
		desc := plan.Descriptor
		source := set.Upsert(desc)
		writeIdentity, err := resolveGitHubPATIdentity(
			ctx, resolver, desc.Key.Host, source, resolvedPATs,
		)
		if err != nil {
			if !plan.Required && errors.Is(err, tokenauth.ErrMissingToken) {
				continue
			}
			return fmt.Errorf(
				"resolve GitHub identity for %s via %s: %w",
				desc.Key.Scope, desc.SafeString(), err,
			)
		}
		readIdentity := writeIdentity
		if app, ok := activeGitHubAppCandidate(desc, plan.GitHubOwner); ok {
			if app.InstallationID <= 0 {
				return fmt.Errorf(
					"resolve GitHub identity for %s via %s: invalid installation id %d",
					desc.Key.Scope, desc.SafeString(), app.InstallationID,
				)
			}
			readIdentity = github.InstallationIdentity(
				desc.Key.Host, app.InstallationID,
			)
		}
		ensureGitHubIdentityRuntime(
			database, budgetPerHour, readIdentity, startup,
		)
		ensureGitHubIdentityRuntime(
			database, budgetPerHour, writeIdentity, startup,
		)
		startup.githubRoutes[desc.Key] = githubCredentialRoute{
			key:           desc.Key,
			source:        source,
			readIdentity:  readIdentity.Key,
			writeIdentity: writeIdentity.Key,
		}
	}
	return nil
}

func buildGitHubRouteClients(startup *providerStartup) error {
	if startup == nil || len(startup.githubRoutes) == 0 {
		return nil
	}
	byHost := make(map[string][]*github.Route)
	for key, configured := range startup.githubRoutes {
		readRuntime := startup.githubIdentities[configured.readIdentity.String()]
		writeRuntime := startup.githubIdentities[configured.writeIdentity.String()]
		if readRuntime == nil || writeRuntime == nil {
			return fmt.Errorf("create GitHub route %s: missing identity runtime", key.Scope)
		}
		client, err := github.NewClient(
			configured.source, key.Host, readRuntime.rest, readRuntime.budget,
			github.WithNotificationAccounting(
				writeRuntime.rest, writeRuntime.budget,
			),
		)
		if err != nil {
			return fmt.Errorf("create GitHub route %s client: %w", key.Scope, err)
		}
		if setter, ok := client.(graphQLRateTrackerSetter); ok {
			setter.SetGraphQLRateTracker(readRuntime.graphql)
		}
		if setter, ok := client.(writeRateTrackerSetter); ok {
			setter.SetWriteRateTracker(writeRuntime.rest)
			setter.SetWriteGraphQLRateTracker(writeRuntime.graphql)
		}
		fetcher := github.NewGraphQLFetcher(
			configured.source, key.Host, readRuntime.graphql, readRuntime.budget,
		)
		configured.client = client
		configured.fetcher = fetcher
		startup.githubRoutes[key] = configured
		owner, name := githubRouteOwnerAndName(key.Scope)
		byHost[key.Host] = append(byHost[key.Host], &github.Route{
			Key:    github.RouteKey{Host: key.Host, Owner: owner, Name: name},
			Client: client, Fetcher: fetcher,
			ReadIdentity:  configured.readIdentity,
			WriteIdentity: configured.writeIdentity,
		})
	}
	for host, routes := range byHost {
		router, err := github.NewHostRouter(host, routes...)
		if err != nil {
			return fmt.Errorf("create GitHub router for %s: %w", host, err)
		}
		client, err := github.NewRoutedClient(router)
		if err != nil {
			return fmt.Errorf("create routed GitHub client for %s: %w", host, err)
		}
		startup.githubRouters[host] = router
		startup.githubClients[host] = client
	}
	return nil
}

func githubRouteOwnerAndName(scope string) (string, string) {
	switch {
	case strings.HasPrefix(scope, "owner:"):
		return strings.TrimPrefix(scope, "owner:"), ""
	case strings.HasPrefix(scope, "repo:"):
		owner, name, _ := strings.Cut(strings.TrimPrefix(scope, "repo:"), "/")
		return owner, name
	default:
		return "", ""
	}
}

func githubCredentialPlans(cfg *config.Config) []config.ProviderTokenSource {
	plans := cfg.ProviderTokenSources()
	out := make([]config.ProviderTokenSource, 0, len(plans))
	for _, plan := range plans {
		if plan.Descriptor.Key.Platform != string(platform.KindGitHub) {
			continue
		}
		out = append(out, plan)
	}
	return out
}

func resolveGitHubPATIdentity(
	ctx context.Context,
	resolver github.IdentityResolver,
	host string,
	source tokenauth.Source,
	cache map[string]github.GitHubIdentity,
) (github.GitHubIdentity, error) {
	desc := source.Descriptor()
	cacheKey := strings.ToLower(strings.TrimSpace(host)) + "\x00" +
		mutationSourceIdentity(desc)
	if identity, ok := cache[cacheKey]; ok {
		return identity, nil
	}
	identity, err := resolver.ResolvePAT(ctx, host, source)
	if err != nil {
		return github.GitHubIdentity{}, err
	}
	cache[cacheKey] = identity
	return identity, nil
}

func mutationSourceIdentity(desc tokenauth.Descriptor) string {
	candidates := make([]tokenauth.Candidate, 0, len(desc.Candidates))
	for _, candidate := range desc.Candidates {
		if candidate.Kind != tokenauth.SourceKindGitHubApp {
			candidates = append(candidates, candidate)
		}
	}
	return (tokenauth.Descriptor{Candidates: candidates}).CanonicalSourceString()
}

func activeGitHubAppCandidate(
	desc tokenauth.Descriptor, owner string,
) (tokenauth.Candidate, bool) {
	for _, candidate := range desc.Candidates {
		if candidate.Kind != tokenauth.SourceKindGitHubApp ||
			candidate.InstallationID == 0 {
			continue
		}
		if candidate.InstallationAccount == "" ||
			(owner != "" && strings.EqualFold(
				candidate.InstallationAccount, owner,
			)) {
			return candidate, true
		}
	}
	return tokenauth.Candidate{}, false
}

func ensureGitHubIdentityRuntime(
	database *db.DB,
	budgetPerHour int,
	identity github.GitHubIdentity,
	startup *providerStartup,
) *githubIdentityRuntime {
	key := identity.Key.String()
	if runtime, ok := startup.githubIdentities[key]; ok {
		return runtime
	}
	runtime := &githubIdentityRuntime{
		identity: identity,
		rest: github.NewPlatformRateTracker(
			database, string(platform.KindGitHub), identity.Key.Host,
			identity.Key.Principal, "rest",
		),
		graphql: github.NewPlatformRateTracker(
			database, string(platform.KindGitHub), identity.Key.Host,
			identity.Key.Principal, "graphql",
		),
	}
	if budgetPerHour > 0 {
		runtime.budget = github.NewSyncBudget(budgetPerHour)
		runtime.rest.SetOnWindowReset(runtime.budget.Reset)
	}
	startup.githubIdentities[key] = runtime
	startup.rateTrackers[github.RateBucketKey(
		string(platform.KindGitHub), identity.Key.Host, identity.Key.Principal,
	)] = runtime.rest
	if runtime.budget != nil {
		startup.budgets[github.RateBucketKey(
			string(platform.KindGitHub), identity.Key.Host, identity.Key.Principal,
		)] = runtime.budget
	}
	return runtime
}

func platformLabel(platformName string) string {
	if meta, ok := platform.MetadataFor(platform.Kind(platformName)); ok {
		return meta.Label
	}
	return platformName
}
