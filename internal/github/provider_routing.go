package github

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

func registryFromGitHubClients(clients map[string]Client) *platform.Registry {
	registry, err := platform.NewRegistry()
	if err != nil {
		panic(fmt.Sprintf("create empty provider registry: %v", err))
	}
	for host, client := range clients {
		if client == nil {
			continue
		}
		provider, err := platformgithub.NewProvider(platformgithub.ProviderConfig{
			Host: canonicalRepoHost(host), Client: client, Clock: time.Now,
			ViewerCacheTTL: authenticatedViewerLoginTTL, Warning: slog.Warn,
		})
		if err != nil {
			panic(fmt.Sprintf("create GitHub provider: %v", err))
		}
		_ = registry.Register(provider)
	}
	return registry
}

func NewProviderRegistry(
	clients map[string]Client,
	providers ...platform.Provider,
) (*platform.Registry, error) {
	registry := registryFromGitHubClients(clients)
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// Identity returns the ref's canonical identity; see platform.RepoRef.Identity.
func (r RepoRef) Identity() platform.RepositoryIdentity {
	return platform.RepositoryIdentity{
		Provider: string(repoPlatform(r)), PlatformHost: repoHost(r), Key: r.Key,
	}.Canonical()
}

func repoPlatform(repo RepoRef) platform.Kind {
	if repo.Platform != "" {
		return repo.Platform
	}
	return platform.KindGitHub
}

func repoHost(repo RepoRef) string {
	if repo.PlatformHost != "" {
		return canonicalRepoHost(repo.PlatformHost)
	}
	if host, ok := platform.DefaultHost(repoPlatform(repo)); ok {
		return host
	}
	return platform.DefaultGitHubHost
}

func (s *Syncer) identityForRepo(repo RepoRef, write bool) (IdentityKey, error) {
	return s.identityForRepoContext(context.Background(), repo, write)
}

func (s *Syncer) identityForRepoContext(
	ctx context.Context, repo RepoRef, write bool,
) (IdentityKey, error) {
	if repoPlatform(repo) != platform.KindGitHub {
		return HostIdentity(repoHost(repo)), nil
	}
	host := repoHost(repo)
	router := s.routers[host]
	if router == nil {
		return HostIdentity(host), nil
	}
	if IsArchiveSyncBudgetContext(ctx) && !write {
		return router.ArchiveIdentityForRepo(repo.Owner, repo.Name)
	}
	if write {
		return router.WriteIdentityForRepo(repo.Owner, repo.Name)
	}
	return router.ReadIdentityForRepo(repo.Owner, repo.Name)
}

func (s *Syncer) archiveIdentityForRepo(repo RepoRef) (IdentityKey, error) {
	if repoPlatform(repo) != platform.KindGitHub {
		return HostIdentity(repoHost(repo)), nil
	}
	host := repoHost(repo)
	router := s.routers[host]
	if router == nil {
		return HostIdentity(host), nil
	}
	return router.ArchiveIdentityForRepo(repo.Owner, repo.Name)
}

func (s *Syncer) bucketKeyForRepo(repo RepoRef, write bool) (string, error) {
	return s.bucketKeyForRepoContext(context.Background(), repo, write)
}

func (s *Syncer) bucketKeyForRepoContext(
	ctx context.Context, repo RepoRef, write bool,
) (string, error) {
	identity, err := s.identityForRepoContext(ctx, repo, write)
	if err != nil {
		return "", err
	}
	return RateBucketKey(
		string(repoPlatform(repo)), identity.Host, identity.Principal,
	), nil
}

// RateTrackerForRepo returns the identity-scoped read tracker for apiType.
func (s *Syncer) RateTrackerForRepo(repo RepoRef, apiType string) (*RateTracker, bool) {
	bucket, err := s.bucketKeyForRepo(repo, false)
	if err != nil {
		return nil, false
	}
	if apiType == "graphql" {
		if fetcher := s.fetcherFor(repo); fetcher != nil && fetcher.RateTracker() != nil {
			return fetcher.RateTracker(), true
		}
		if rt := s.GQLRateTrackers()[bucket]; rt != nil {
			return rt, true
		}
	}
	rt := s.rateTrackers[bucket]
	if rt == nil {
		return nil, false
	}
	return rt, true
}

// ReadIdentityForRepo returns the restart-bound read identity selected for
// repo when a GitHub route router is configured.
func (s *Syncer) ReadIdentityForRepo(repo RepoRef) (IdentityKey, bool) {
	if repoPlatform(repo) != platform.KindGitHub {
		return IdentityKey{}, false
	}
	router := s.routers[repoHost(repo)]
	if router == nil {
		return IdentityKey{}, false
	}
	identity, err := router.ReadIdentityForRepo(repo.Owner, repo.Name)
	return identity, err == nil && identity.Principal != ""
}

// HasGitHubRouter reports whether startup configured route selection for the
// repository's GitHub host. A configured router can still reject a repository
// with MissingRouteError when no exact, owner, or fallback route matches.
func (s *Syncer) HasGitHubRouter(repo RepoRef) bool {
	if s == nil || repoPlatform(repo) != platform.KindGitHub {
		return false
	}
	return s.routers[repoHost(repo)] != nil
}

// WriteIdentityForRepo returns the restart-bound mutation identity selected
// for repo. App-only routes have no write identity and remain mutation-disabled
// until restart establishes one.
func (s *Syncer) WriteIdentityForRepo(repo RepoRef) (IdentityKey, bool) {
	identity, err := s.identityForRepo(repo, true)
	return identity, err == nil && identity.Principal != ""
}

type writeCredentialProber interface {
	ProbeWriteCredential(context.Context) error
}

func (s *Syncer) writeCredentialProberForRepo(
	repo RepoRef,
) (*Route, writeCredentialProber, error) {
	if repoPlatform(repo) != platform.KindGitHub {
		return nil, nil, nil
	}
	router := s.routers[repoHost(repo)]
	if router == nil {
		return nil, nil, nil
	}
	route, err := router.RouteForRepo(repo.Owner, repo.Name)
	if err != nil {
		return nil, nil, err
	}
	client := route.Client
	if route.WriteSnapshotClient != nil {
		client = route.WriteSnapshotClient
	}
	prober, ok := client.(writeCredentialProber)
	if !ok {
		return route, nil, nil
	}
	return route, prober, nil
}

// WriteCredentialProbeKeyForRepo identifies the routed mutation credential
// whose live availability is probed. Owner routes share one key across their
// repositories, while selected-installation routes remain repository-exact.
func (s *Syncer) WriteCredentialProbeKeyForRepo(repo RepoRef) (string, error) {
	route, prober, err := s.writeCredentialProberForRepo(repo)
	if err != nil || route == nil || prober == nil {
		return "", err
	}
	return fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s",
		route.Key.Host, route.Key.Owner, route.Key.Name, route.WriteIdentity.String(),
	), nil
}

// ProbeWriteCredentialForRepo resolves the live mutation-bound credential for
// repo. The route identity is fixed at startup, but the underlying token file,
// environment, or gh CLI source can disappear or rotate afterwards.
func (s *Syncer) ProbeWriteCredentialForRepo(
	ctx context.Context, repo RepoRef,
) error {
	_, prober, err := s.writeCredentialProberForRepo(repo)
	if err != nil {
		return err
	}
	if prober == nil {
		return nil
	}
	return prober.ProbeWriteCredential(ctx)
}

// WriteRateTrackerForRepo returns the tracker for the credential that
// authenticates repository mutations.
func (s *Syncer) WriteRateTrackerForRepo(repo RepoRef, apiType string) (*RateTracker, bool) {
	identity, ok := s.WriteIdentityForRepo(repo)
	if !ok {
		return nil, false
	}
	bucket := RateBucketKey(string(repoPlatform(repo)), identity.Host, identity.Principal)
	trackers := s.writeRateTrackers
	if apiType == "graphql" {
		trackers = s.writeGQLRateTrackers
	}
	if rt := trackers[bucket]; rt != nil {
		return rt, true
	}
	return s.RateTrackerForRepo(repo, apiType)
}

// BudgetForRepo returns the background sync budget owned by the repository's
// effective read identity.
func (s *Syncer) BudgetForRepo(repo RepoRef) (*SyncBudget, bool) {
	bucket, err := s.bucketKeyForRepo(repo, false)
	if err != nil {
		return nil, false
	}
	budget := s.budgets[bucket]
	if budget == nil {
		return nil, false
	}
	return budget, true
}

func platformRepoRef(repo RepoRef) platform.RepoRef {
	repoPath := repo.RepoPath
	if repoPath == "" {
		repoPath = repo.Owner + "/" + repo.Name
	}
	return platform.RepoRef{
		Platform:      repoPlatform(repo),
		Host:          repoHost(repo),
		Owner:         repo.Owner,
		Name:          repo.Name,
		RepoPath:      repoPath,
		Key:           repo.Key,
		WebURL:        repo.WebURL,
		CloneURL:      repo.CloneURL,
		DefaultBranch: repo.DefaultBranch,
	}
}

func cloneRemoteURL(repo RepoRef) string {
	if repo.CloneURL != "" {
		return repo.CloneURL
	}
	repoPath := repo.RepoPath
	if repoPath == "" {
		repoPath = strings.Trim(repo.Owner+"/"+repo.Name, "/")
	}
	return platform.DefaultCloneURL(repoPlatform(repo), repoHost(repo), repoPath)
}

func withCloneRepositoryIdentity(ctx context.Context, repo RepoRef) context.Context {
	return gitclone.WithRepositoryIdentity(ctx, repo.Identity())
}

func (s *Syncer) ensureClone(ctx context.Context, repo RepoRef) error {
	err := s.clones.EnsureCloneValidated(
		ctx,
		string(repoPlatform(repo)),
		repoHost(repo),
		repo.Owner,
		repo.Name,
		cloneRemoteURL(repo),
		nil,
	)
	return s.recordGitAccess(repo, err)
}

func (s *Syncer) optionalGitHubClientFor(repo RepoRef) (Client, bool) {
	client, err := s.clientFor(repo)
	if err != nil {
		return nil, false
	}
	return client, true
}

// clientFor returns the legacy GitHub Client for the given repo's host.
// Repos with an empty host default to "github.com".
func clientForRegistry(registry *platform.Registry, repo RepoRef) (Client, error) {
	host := repoHost(repo)
	provider, err := registry.Provider(repoPlatform(repo), host)
	if err != nil {
		return nil, err
	}
	legacy, ok := provider.(interface{ GitHubClient() platformgithub.API })
	if !ok || legacy.GitHubClient() == nil {
		return nil, fmt.Errorf("no GitHub client configured for host %s", host)
	}
	return legacy.GitHubClient(), nil
}

func (s *Syncer) clientFor(repo RepoRef) (Client, error) {
	return clientForRegistry(s.clients, repo)
}

func (s *Syncer) mergeRequestReaderFor(repo RepoRef) (platform.MergeRequestReader, error) {
	return s.clients.MergeRequestReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) issueReaderFor(repo RepoRef) (platform.IssueReader, error) {
	return s.clients.IssueReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) labelReaderFor(repo RepoRef) (platform.LabelReader, error) {
	return s.clients.LabelReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) releaseReaderFor(repo RepoRef) (platform.ReleaseReader, error) {
	return s.clients.ReleaseReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) tagReaderFor(repo RepoRef) (platform.TagReader, error) {
	return s.clients.TagReader(repoPlatform(repo), repoHost(repo))
}

func (s *Syncer) ciReaderFor(repo RepoRef) (platform.CIReader, error) {
	return s.clients.CIReader(repoPlatform(repo), repoHost(repo))
}

// ClientForRepo returns the Client for a tracked repo by
// owner/name, or an error if the repo is not tracked.
func (s *Syncer) ClientForRepo(
	owner, name string,
) (Client, error) {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, r := range s.repos {
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) {
			return s.clientFor(r)
		}
	}
	return nil, fmt.Errorf(
		"repo %s/%s is %w", owner, name, ErrRepoNotTracked,
	)
}

// ClientForHost returns the Client for a specific host,
// or an error if no client is configured for that host.
func (s *Syncer) ClientForHost(
	host string,
) (Client, error) {
	return s.clientFor(RepoRef{PlatformHost: host})
}

// DirectClientForHost returns a legacy GitHub client for an explicit
// foreground provider operation.
func (s *Syncer) DirectClientForHost(host string) (Client, error) {
	return clientForRegistry(s.directClients, RepoRef{PlatformHost: host})
}

func (s *Syncer) ProviderCapabilities(
	kind platform.Kind,
	host string,
) (platform.Capabilities, error) {
	if kind == "" {
		kind = platform.KindGitHub
	}
	if strings.TrimSpace(host) == "" {
		defaultHost, ok := platform.DefaultHost(kind)
		if !ok {
			return platform.Capabilities{}, platform.ProviderNotConfigured(kind, "")
		}
		host = defaultHost
	}
	return s.directClients.Capabilities(kind, canonicalRepoHost(host))
}

func (s *Syncer) RepositoryReader(
	kind platform.Kind,
	host string,
) (platform.RepositoryReader, error) {
	return s.directClients.RepositoryReader(kind, canonicalRepoHost(host))
}

// Registry returns the boot-time provider registry for explicit foreground
// operations. Callers must not mutate it.
func (s *Syncer) Registry() *platform.Registry {
	return s.directClients
}

// SyncRegistry returns the provider-gated registry used by refresh work.
func (s *Syncer) SyncRegistry() *platform.Registry {
	return s.clients
}

func (s *Syncer) LabelReader(
	kind platform.Kind,
	host string,
) (platform.LabelReader, error) {
	return s.directClients.LabelReader(kind, canonicalRepoHost(host))
}

func (s *Syncer) CommentMutator(
	kind platform.Kind,
	host string,
) (platform.CommentMutator, error) {
	return s.directClients.CommentMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) StateMutator(
	kind platform.Kind,
	host string,
) (platform.StateMutator, error) {
	return s.directClients.StateMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) MergeMutator(
	kind platform.Kind,
	host string,
) (platform.MergeMutator, error) {
	return s.directClients.MergeMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) MergeQueueMutator(
	kind platform.Kind,
	host string,
) (platform.MergeQueueMutator, error) {
	return s.directClients.MergeQueueMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) WorkflowApprovalMutator(
	kind platform.Kind,
	host string,
) (platform.WorkflowApprovalMutator, error) {
	return s.directClients.WorkflowApprovalMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) ReadyForReviewMutator(
	kind platform.Kind,
	host string,
) (platform.ReadyForReviewMutator, error) {
	return s.directClients.ReadyForReviewMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) DraftMutator(
	kind platform.Kind,
	host string,
) (platform.DraftMutator, error) {
	return s.directClients.DraftMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) IssueMutator(
	kind platform.Kind,
	host string,
) (platform.IssueMutator, error) {
	return s.directClients.IssueMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) LabelMutator(
	kind platform.Kind,
	host string,
) (platform.LabelMutator, error) {
	return s.directClients.LabelMutator(kind, canonicalRepoHost(host))
}

// MergeRequestReader reads merge requests for user-initiated requests that
// need the provider's live state rather than the last sync.
func (s *Syncer) MergeRequestReader(
	kind platform.Kind,
	host string,
) (platform.MergeRequestReader, error) {
	return s.directClients.MergeRequestReader(kind, canonicalRepoHost(host))
}

// IssueReader reads issues for user-initiated requests that need the
// provider's live state rather than the last sync.
func (s *Syncer) IssueReader(
	kind platform.Kind,
	host string,
) (platform.IssueReader, error) {
	return s.directClients.IssueReader(kind, canonicalRepoHost(host))
}

func (s *Syncer) ReviewMutator(
	kind platform.Kind,
	host string,
) (platform.ReviewMutator, error) {
	return s.directClients.ReviewMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) RequestChangesMutator(
	kind platform.Kind,
	host string,
) (platform.RequestChangesMutator, error) {
	return s.directClients.RequestChangesMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) AssigneeMutator(
	kind platform.Kind,
	host string,
) (platform.AssigneeMutator, error) {
	return s.directClients.AssigneeMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) ReviewerMutator(
	kind platform.Kind,
	host string,
) (platform.ReviewerMutator, error) {
	return s.directClients.ReviewerMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) DiffReviewDraftMutator(
	kind platform.Kind,
	host string,
) (platform.DiffReviewDraftMutator, error) {
	return s.directClients.DiffReviewDraftMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) ReviewSuggestionApplier(
	kind platform.Kind,
	host string,
) (platform.ReviewSuggestionApplier, error) {
	return s.directClients.ReviewSuggestionApplier(kind, canonicalRepoHost(host))
}

func (s *Syncer) DiffReviewThreadResolver(
	kind platform.Kind,
	host string,
) (platform.DiffReviewThreadResolver, error) {
	return s.directClients.DiffReviewThreadResolver(kind, canonicalRepoHost(host))
}

func (s *Syncer) MergeRequestReviewThreadReader(
	kind platform.Kind,
	host string,
) (platform.MergeRequestReviewThreadReader, error) {
	reader, err := s.directClients.MergeRequestReviewThreadReader(kind, canonicalRepoHost(host))
	if err != nil {
		return nil, err
	}
	return syncGatedReviewThreadReader{syncer: s, reader: reader}, nil
}

type syncGatedReviewThreadReader struct {
	syncer *Syncer
	reader platform.MergeRequestReviewThreadReader
}

func (r syncGatedReviewThreadReader) ListMergeRequestReviewThreads(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestReviewThread, error) {
	if err := r.syncer.syncDisabledError(); err != nil {
		return nil, err
	}
	return r.reader.ListMergeRequestReviewThreads(ctx, ref, number)
}

func (s *Syncer) MergeRequestContentMutator(
	kind platform.Kind,
	host string,
) (platform.MergeRequestContentMutator, error) {
	return s.directClients.MergeRequestContentMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) IssueContentMutator(
	kind platform.Kind,
	host string,
) (platform.IssueContentMutator, error) {
	return s.directClients.IssueContentMutator(kind, canonicalRepoHost(host))
}

func (s *Syncer) ResolveConfiguredRepo(
	ctx context.Context,
	repo config.Repo,
) (ConfiguredRepoStatus, []RepoRef, error) {
	return ResolveConfiguredRepoWithRegistry(ctx, s.directClients, repo)
}

func (s *Syncer) ResolveConfiguredRepoForSync(
	ctx context.Context,
	repo config.Repo,
) (ConfiguredRepoStatus, []RepoRef, error) {
	return ResolveConfiguredRepoWithRegistry(ctx, s.clients, repo)
}
