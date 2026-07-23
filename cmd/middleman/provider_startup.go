package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

type mutationTokenSource struct {
	tokenauth.Source
}

func (s mutationTokenSource) Token(ctx context.Context) (string, error) {
	return s.Source.Token(tokenauth.WithMutationAuth(ctx))
}

type identityBoundMutationTokenSource struct {
	tokenauth.Source
	writeIdentity github.IdentityKey
}

func (s identityBoundMutationTokenSource) Token(ctx context.Context) (string, error) {
	if s.writeIdentity.Principal == "" {
		return "", github.ErrMissingWriteIdentity
	}
	return s.Source.Token(tokenauth.WithMutationAuth(ctx))
}

type githubCredentialRoute struct {
	key            tokenauth.Key
	source         tokenauth.Source
	client         github.Client
	fetcher        *github.GraphQLFetcher
	discoveryOwner string
	readIdentity   github.IdentityKey
	writeIdentity  github.IdentityKey
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
	ratePrincipalLabels  map[string]string
}

func (s *providerStartup) SourceForRepo(
	platformName, host, owner, name string,
) tokenauth.Source {
	if s == nil {
		return nil
	}
	platformName = strings.ToLower(strings.TrimSpace(platformName))
	host = strings.ToLower(strings.TrimSpace(host))
	providerSource := s.cloneSources[tokenauth.Key{
		Platform: platformName,
		Host:     host,
	}]
	if platformName != string(platform.KindGitHub) {
		return providerSource
	}
	owner = strings.ToLower(strings.TrimSpace(owner))
	name = strings.ToLower(strings.TrimSpace(name))
	for _, scope := range []string{
		"repo:" + owner + "/" + name,
		"owner:" + owner,
		"",
	} {
		if route, ok := s.githubRoutes[tokenauth.Key{
			Platform: string(platform.KindGitHub), Host: host, Scope: scope,
		}]; ok && route.source != nil {
			return identityBoundMutationTokenSource{
				Source: route.source, writeIdentity: route.writeIdentity,
			}
		}
	}
	if s.githubRouters[host] != nil {
		return nil
	}
	return providerSource
}

func (s *providerStartup) FallbackSource(host string) tokenauth.Source {
	if s == nil {
		return nil
	}
	host = strings.ToLower(strings.TrimSpace(host))
	clone := s.cloneAuth[host]
	// A present-but-empty host chain means providers sharing this hostname
	// disagree (Config.CloneTokenDescriptors disabled the ownerless
	// fallback); the GitHub fallback route must not resurrect it, or an
	// ownerless operation would expose GitHub's credential for work that may
	// belong to another provider.
	if clone != nil && len(clone.Descriptor().Candidates) == 0 {
		return clone
	}
	if route, ok := s.githubRoutes[tokenauth.Key{
		Platform: string(platform.KindGitHub), Host: host,
	}]; ok && route.source != nil {
		return identityBoundMutationTokenSource{
			Source: route.source, writeIdentity: route.writeIdentity,
		}
	}
	return clone
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
	if err := cfg.ValidateRepoTokenSourceConsistency(); err != nil {
		return nil, err
	}
	providerSources := make(map[string]tokenauth.Source, len(cfg.Repos)+len(cfg.Platforms)+1)
	add := func(plan config.ProviderTokenSource) error {
		desc := plan.Descriptor
		key := providerHostKey(desc.Key.Platform, desc.Key.Host)
		_, seen := providerSources[key]
		src := set.Upsert(desc)
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
	if err := cfg.ValidateRepoTokenSourceConsistency(); err != nil {
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
		ratePrincipalLabels:  make(map[string]string),
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
		routedGitHub := platformName == string(platform.KindGitHub) && startup.githubClients[host] != nil
		if !routedGitHub {
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
	// Ownerless Git operations may use only the explicit host fallback chain.
	// Never derive this map from the first provider source on a host because
	// that source can be an owner-scoped GitHub PAT. Scoped repository Git
	// operations select their own route through providerStartup.SourceForRepo.
	for _, desc := range cfg.CloneTokenDescriptors() {
		startup.cloneAuth[desc.Key.Host] = set.Upsert(desc)
	}
	for _, source := range providerSources {
		if source == nil {
			continue
		}
		desc := source.Descriptor()
		host := desc.Key.Host
		if startup.cloneAuth[host] != nil {
			continue
		}
		if desc.Key.Platform == string(platform.KindGitHub) && desc.Key.Scope != "" {
			continue
		}
		desc.Key = tokenauth.CloneKey(host)
		startup.cloneAuth[host] = set.Upsert(desc)
	}
	registry, err := github.NewProviderRegistry(clients, providers...)
	if err != nil {
		return providerStartup{}, fmt.Errorf("create provider registry: %w", err)
	}
	startup.registry = registry
	for host := range githubHosts {
		if startup.githubRouters[host] != nil {
			continue
		}
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
	requiredHosts := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		if plan.Required {
			requiredHosts[plan.Descriptor.Key.Host] = struct{}{}
		}
	}
	resolvedPATs := make(map[string]resolvedGitHubPAT, len(plans))
	for _, plan := range plans {
		desc := plan.Descriptor
		// Scoped routes already carry every credential needed for their
		// repositories, so an implicit default env/gh CLI fallback is probed
		// best-effort: a valid token keeps ownerless APIs served, while a
		// missing or invalid one is skipped with a warning instead of
		// failing startup. Explicit host fallbacks still fail hard.
		bestEffort := !plan.Required && desc.Key.Scope == "" &&
			len(requiredHosts) > 0 && !hasExplicitGitHubFallback(cfg, desc.Key.Host)
		var source tokenauth.Source = set.Upsert(desc)
		app, hasApp := activeGitHubAppCandidate(desc, plan.GitHubOwner)
		resolvedWrite, err := resolveGitHubPATIdentity(
			ctx, resolver, desc.Key.Host, source, resolvedPATs,
		)
		writeIdentity := resolvedWrite.identity
		if err != nil {
			if errors.Is(err, tokenauth.ErrMissingToken) && hasApp {
				writeIdentity = github.GitHubIdentity{}
			} else if !plan.Required && errors.Is(err, tokenauth.ErrMissingToken) {
				continue
			} else if bestEffort {
				slog.Warn(
					"skipping implicit GitHub host fallback; ownerless APIs stay unrouted until it resolves",
					"host", desc.Key.Host,
					"source", desc.SafeString(),
					"error", err,
				)
				continue
			} else {
				return fmt.Errorf(
					"resolve GitHub identity for %s via %s: %w",
					desc.Key.Scope, desc.SafeString(), err,
				)
			}
		}
		readIdentity := writeIdentity
		if hasApp {
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
		if writeIdentity.Key.Principal != "" {
			ensureGitHubIdentityRuntime(
				database, budgetPerHour, writeIdentity, startup,
			)
		}
		if writeIdentity.Key.Principal != "" {
			source = github.BindSourceIdentity(
				source, desc.Key.Host, writeIdentity.Key, resolvedWrite.token, resolver,
			)
		}
		discoveryOwner := ""
		if hasApp && strings.HasPrefix(desc.Key.Scope, "repo:") {
			discoveryOwner = app.InstallationAccount
		}
		startup.githubRoutes[desc.Key] = githubCredentialRoute{
			key:            desc.Key,
			source:         source,
			discoveryOwner: discoveryOwner,
			readIdentity:   readIdentity.Key,
			writeIdentity:  writeIdentity.Key,
		}
	}
	return nil
}

func hasExplicitGitHubFallback(cfg *config.Config, host string) bool {
	if cfg == nil {
		return false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	// Loaded configs always carry a github_token_env value because Load
	// defaults it and Save writes it back; only a non-default name marks a
	// deliberate github.com fallback that may hold startup hostage.
	if host == platform.DefaultGitHubHost && cfg.HasExplicitGitHubTokenEnv() {
		return true
	}
	for _, configured := range cfg.Platforms {
		if strings.EqualFold(configured.Type, string(platform.KindGitHub)) &&
			strings.EqualFold(configured.Host, host) &&
			(strings.TrimSpace(configured.TokenEnv) != "" || strings.TrimSpace(configured.TokenFile) != "") {
			return true
		}
	}
	return false
}

func buildGitHubRouteClients(startup *providerStartup) error {
	if startup == nil || len(startup.githubRoutes) == 0 {
		return nil
	}
	byHost := make(map[string][]*github.Route)
	discoveryRoutes := make(map[string]struct{})
	for key, configured := range startup.githubRoutes {
		readRuntime := startup.githubIdentities[configured.readIdentity.String()]
		var writeRuntime *githubIdentityRuntime
		if configured.writeIdentity.Principal != "" {
			writeRuntime = startup.githubIdentities[configured.writeIdentity.String()]
		}
		if readRuntime == nil || (configured.writeIdentity.Principal != "" && writeRuntime == nil) {
			return fmt.Errorf("create GitHub route %s: missing identity runtime", key.Scope)
		}
		clientOptions := []github.ClientOption{}
		if writeRuntime == nil {
			clientOptions = append(clientOptions, github.WithMutationsDisabled())
		} else {
			clientOptions = append(clientOptions, github.WithNotificationAccounting(
				writeRuntime.rest, writeRuntime.budget,
			))
		}
		client, err := github.NewClient(
			configured.source, key.Host, readRuntime.rest, readRuntime.budget,
			clientOptions...,
		)
		if err != nil {
			return fmt.Errorf("create GitHub route %s client: %w", key.Scope, err)
		}
		if setter, ok := client.(graphQLRateTrackerSetter); ok {
			setter.SetGraphQLRateTracker(readRuntime.graphql)
		}
		if writeRuntime != nil {
			if setter, ok := client.(writeRateTrackerSetter); ok {
				setter.SetWriteRateTracker(writeRuntime.rest)
				setter.SetWriteGraphQLRateTracker(writeRuntime.graphql)
			}
			writeBucket := github.RateBucketKey(
				string(platform.KindGitHub), key.Host,
				configured.writeIdentity.Principal,
			)
			startup.writeRateTrackers[writeBucket] = writeRuntime.rest
			startup.writeGQLRateTrackers[writeBucket] = writeRuntime.graphql
		}
		fetcher := github.NewGraphQLFetcher(
			configured.source, key.Host, readRuntime.graphql, readRuntime.budget,
		)
		var writeSnapshotClient github.Client
		if writeRuntime != nil && configured.writeIdentity != configured.readIdentity {
			writeSnapshotClient, err = github.NewClient(
				mutationTokenSource{Source: configured.source}, key.Host,
				writeRuntime.rest, nil,
			)
			if err != nil {
				return fmt.Errorf("create GitHub route %s write snapshot client: %w", key.Scope, err)
			}
			if setter, ok := writeSnapshotClient.(graphQLRateTrackerSetter); ok {
				setter.SetGraphQLRateTracker(writeRuntime.graphql)
			}
		}
		configured.client = client
		configured.fetcher = fetcher
		startup.githubRoutes[key] = configured
		owner, name := githubRouteOwnerAndName(key.Scope)
		var discoveryClient github.Client
		if configured.discoveryOwner != "" {
			discoveryKey := key.Host + "\x00" + strings.ToLower(configured.discoveryOwner)
			if _, exists := discoveryRoutes[discoveryKey]; !exists {
				discoveryRoutes[discoveryKey] = struct{}{}
				discoveryClient = client
			}
		}
		byHost[key.Host] = append(byHost[key.Host], &github.Route{
			Key:    github.RouteKey{Host: key.Host, Owner: owner, Name: name},
			Client: client, DiscoveryClient: discoveryClient,
			WriteSnapshotClient: writeSnapshotClient,
			Fetcher:             fetcher, ReadIdentity: configured.readIdentity,
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

type resolvedGitHubPAT struct {
	identity github.GitHubIdentity
	token    string
}

func resolveGitHubPATIdentity(
	ctx context.Context,
	resolver github.IdentityResolver,
	host string,
	source tokenauth.Source,
	cache map[string]resolvedGitHubPAT,
) (resolvedGitHubPAT, error) {
	desc := source.Descriptor()
	cacheKey := strings.ToLower(strings.TrimSpace(host)) + "\x00" +
		mutationSourceIdentity(desc)
	if resolved, ok := cache[cacheKey]; ok {
		return resolved, nil
	}
	identity, token, err := resolver.ResolvePAT(ctx, host, source)
	if err != nil {
		return resolvedGitHubPAT{}, err
	}
	resolved := resolvedGitHubPAT{identity: identity, token: token}
	cache[cacheKey] = resolved
	return resolved, nil
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
	bucket := github.RateBucketKey(
		string(platform.KindGitHub), identity.Key.Host, identity.Key.Principal,
	)
	startup.ratePrincipalLabels[bucket] = identity.Label()
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
