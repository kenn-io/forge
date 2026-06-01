package github

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/platform"
)

var ErrConfiguredRepoArchived = errors.New("configured repo archived")

func canonicalRepoName(name string) string {
	return strings.ToLower(name)
}

func canonicalRepoOwner(owner string) string {
	return strings.ToLower(owner)
}

func canonicalRepoHost(host string) string {
	if host == "" {
		host = "github.com"
	}
	return strings.ToLower(host)
}

func canonicalRepoRef(repo RepoRef) RepoRef {
	kind := repoPlatform(repo)
	out := RepoRef{
		Owner:              strings.TrimSpace(repo.Owner),
		Name:               strings.TrimSpace(repo.Name),
		PlatformHost:       canonicalRepoHost(repo.PlatformHost),
		RepoPath:           strings.TrimSpace(repo.RepoPath),
		PlatformRepoID:     repo.PlatformRepoID,
		PlatformExternalID: strings.TrimSpace(repo.PlatformExternalID),
		WebURL:             strings.TrimSpace(repo.WebURL),
		CloneURL:           strings.TrimSpace(repo.CloneURL),
		DefaultBranch:      strings.TrimSpace(repo.DefaultBranch),
	}
	if kind == platform.KindGitHub {
		out.Owner = canonicalRepoOwner(out.Owner)
		out.Name = canonicalRepoName(out.Name)
		if out.RepoPath != "" {
			out.RepoPath = canonicalRepoName(out.RepoPath)
		}
	} else {
		out.Platform = kind
	}
	if out.RepoPath == "" {
		out.RepoPath = out.Owner + "/" + out.Name
	}
	return out
}

func canonicalRepoPattern(pattern string) string {
	return strings.ToLower(pattern)
}

type ConfiguredRepoStatus struct {
	Provider         string `json:"provider"`
	PlatformHost     string `json:"platform_host"`
	Owner            string `json:"owner"`
	Name             string `json:"name"`
	RepoPath         string `json:"repo_path"`
	WorktreeBasePath string `json:"worktree_base_path,omitempty"`
	IsGlob           bool   `json:"is_glob"`
	MatchedRepoCount int    `json:"matched_repo_count"`
}

type ResolveConfiguredReposResult struct {
	Configured []ConfiguredRepoStatus
	Expanded   []RepoRef
	Warnings   []error
}

func FallbackConfiguredRepoRefs(
	previous []RepoRef,
	raw config.Repo,
) []RepoRef {
	kind := platform.Kind(raw.PlatformOrDefault())
	host := raw.PlatformHostOrDefault()
	repoPath := configuredRepoPath(raw)
	if !raw.HasNameGlob() {
		for _, repo := range previous {
			if repoPlatform(repo) == kind &&
				sameConfiguredRepoHost(repoHost(repo), host) &&
				strings.EqualFold(repoPathOrFullName(repo), repoPath) {
				return []RepoRef{repo}
			}
		}
		return []RepoRef{fallbackRepoRef(raw, kind, host)}
	}

	fallback := make([]RepoRef, 0)
	for _, repo := range previous {
		if repoPlatform(repo) != kind ||
			!sameConfiguredRepoHost(repoHost(repo), host) ||
			!strings.EqualFold(repo.Owner, raw.Owner) {
			continue
		}
		matched, err := path.Match(
			canonicalRepoPattern(raw.Name),
			canonicalRepoName(repo.Name),
		)
		if err != nil || !matched {
			continue
		}
		fallback = append(fallback, repo)
	}
	return fallback
}

func fallbackRepoRef(raw config.Repo, kind platform.Kind, host string) RepoRef {
	repo := RepoRef{
		Owner:        strings.TrimSpace(raw.Owner),
		Name:         strings.TrimSpace(raw.Name),
		PlatformHost: strings.ToLower(strings.TrimSpace(host)),
		RepoPath:     strings.TrimSpace(configuredRepoPath(raw)),
	}
	if kind == "" {
		kind = platform.KindGitHub
	}
	if kind == platform.KindGitHub {
		repo.Owner = canonicalRepoOwner(repo.Owner)
		repo.Name = canonicalRepoName(repo.Name)
		repo.PlatformHost = canonicalRepoHost(repo.PlatformHost)
		return repo
	}
	repo.Platform = kind
	return repo
}

func ResolveConfiguredRepos(
	ctx context.Context,
	clients map[string]Client,
	repos []config.Repo,
) ResolveConfiguredReposResult {
	return resolveConfiguredRepos(ctx, registryFromGitHubClients(clients), repos)
}

func ResolveConfiguredReposWithRegistry(
	ctx context.Context,
	registry *platform.Registry,
	repos []config.Repo,
) ResolveConfiguredReposResult {
	return resolveConfiguredRepos(ctx, registry, repos)
}

func resolveConfiguredRepos(
	ctx context.Context,
	registry *platform.Registry,
	repos []config.Repo,
) ResolveConfiguredReposResult {
	seen := make(map[string]struct{})
	result := ResolveConfiguredReposResult{
		Configured: make([]ConfiguredRepoStatus, 0, len(repos)),
	}

	for _, raw := range repos {
		status, expanded, err := resolveConfiguredRepo(
			ctx, registry, raw,
		)
		if err != nil {
			status.MatchedRepoCount = 0
			result.Warnings = append(result.Warnings, err)
		}
		result.Configured = append(result.Configured, status)
		for _, repo := range expanded {
			appendExpandedRepo(&result.Expanded, seen, repo)
		}
	}

	return result
}

func ResolveConfiguredRepo(
	ctx context.Context,
	clients map[string]Client,
	repo config.Repo,
) (ConfiguredRepoStatus, []RepoRef, error) {
	return resolveConfiguredRepo(ctx, registryFromGitHubClients(clients), repo)
}

func ResolveConfiguredRepoWithRegistry(
	ctx context.Context,
	registry *platform.Registry,
	repo config.Repo,
) (ConfiguredRepoStatus, []RepoRef, error) {
	return resolveConfiguredRepo(ctx, registry, repo)
}

func resolveConfiguredRepo(
	ctx context.Context,
	registry *platform.Registry,
	raw config.Repo,
) (ConfiguredRepoStatus, []RepoRef, error) {
	status := ConfiguredRepoStatus{
		Provider:     raw.PlatformOrDefault(),
		PlatformHost: raw.PlatformHostOrDefault(),
		Owner:        raw.Owner,
		Name:         raw.Name,
		RepoPath:     configuredRepoPath(raw),
		IsGlob:       raw.HasNameGlob(),
	}
	kind := platform.Kind(raw.PlatformOrDefault())
	host := raw.PlatformHostOrDefault()
	repoPath := configuredRepoPath(raw)
	reader, err := registry.RepositoryReader(kind, host)
	if err != nil {
		return status, nil, err
	}

	if !status.IsGlob {
		repo, err := reader.GetRepository(ctx, platform.RepoRef{
			Platform: kind,
			Host:     host,
			Owner:    raw.Owner,
			Name:     raw.Name,
			RepoPath: repoPath,
		})
		if err != nil {
			return status, nil, fmt.Errorf(
				"resolve configured repo %s/%s: %w",
				raw.Owner, raw.Name, err,
			)
		}
		if repo.Archived {
			return status, nil, fmt.Errorf(
				"%w: %s/%s",
				ErrConfiguredRepoArchived, raw.Owner, raw.Name,
			)
		}
		status.MatchedRepoCount = 1
		return status, []RepoRef{repoRefFromRepository(raw, kind, host, repo)}, nil
	}

	repos, err := reader.ListRepositories(ctx, raw.Owner, platform.RepositoryListOptions{})
	if err != nil {
		return status, nil, fmt.Errorf(
			"resolve configured repo glob %s/%s: %w",
			raw.Owner, raw.Name, err,
		)
	}

	matches := make([]RepoRef, 0, len(repos))
	for _, repo := range repos {
		if repo.Archived {
			continue
		}
		repoName := repo.Ref.Name
		if repoName == "" {
			repoName = repo.Ref.DisplayName()
		}
		matched, err := path.Match(
			canonicalRepoName(raw.Name),
			canonicalRepoName(repoName),
		)
		if err != nil {
			return status, nil, fmt.Errorf(
				"invalid repo glob %s/%s: %w",
				raw.Owner, raw.Name, err,
			)
		}
		if !matched {
			continue
		}
		matches = append(matches, repoRefFromRepository(raw, kind, host, repo))
	}
	status.MatchedRepoCount = len(matches)
	return status, matches, nil
}

func configuredRepoPath(raw config.Repo) string {
	if strings.TrimSpace(raw.RepoPath) != "" {
		return strings.TrimSpace(raw.RepoPath)
	}
	return raw.Owner + "/" + raw.Name
}

func repoPathOrFullName(repo RepoRef) string {
	if strings.TrimSpace(repo.RepoPath) != "" {
		return strings.TrimSpace(repo.RepoPath)
	}
	return repo.Owner + "/" + repo.Name
}

func repoRefFromRepository(
	raw config.Repo,
	kind platform.Kind,
	host string,
	repo platform.Repository,
) RepoRef {
	owner := repo.Ref.Owner
	if owner == "" {
		owner = raw.Owner
	}
	name := repo.Ref.Name
	if name == "" {
		name = raw.Name
	}
	ref := RepoRef{
		Owner:              strings.TrimSpace(owner),
		Name:               strings.TrimSpace(name),
		PlatformHost:       canonicalRepoHost(host),
		RepoPath:           strings.TrimSpace(repo.Ref.RepoPath),
		PlatformRepoID:     repo.PlatformID,
		PlatformExternalID: repo.PlatformExternalID,
		WebURL:             repo.WebURL,
		CloneURL:           repo.CloneURL,
		DefaultBranch:      repo.DefaultBranch,
	}
	if ref.PlatformRepoID == 0 {
		ref.PlatformRepoID = repo.Ref.PlatformID
	}
	if ref.PlatformExternalID == "" {
		ref.PlatformExternalID = repo.Ref.PlatformExternalID
	}
	if ref.WebURL == "" {
		ref.WebURL = repo.Ref.WebURL
	}
	if ref.CloneURL == "" {
		ref.CloneURL = repo.Ref.CloneURL
	}
	if ref.DefaultBranch == "" {
		ref.DefaultBranch = repo.Ref.DefaultBranch
	}
	if kind != platform.KindGitHub {
		ref.Platform = kind
	} else {
		ref.Owner = canonicalRepoOwner(ref.Owner)
		ref.Name = canonicalRepoName(ref.Name)
		ref.RepoPath = canonicalRepoName(ref.RepoPath)
	}
	if ref.RepoPath == "" {
		ref.RepoPath = ref.Owner + "/" + ref.Name
	}
	return ref
}

func appendExpandedRepo(
	dst *[]RepoRef,
	seen map[string]struct{},
	repo RepoRef,
) {
	repo = canonicalRepoRef(repo)
	key := string(repoPlatform(repo)) + "\x00" + repo.PlatformHost + "\x00" + repo.Owner + "\x00" + repo.Name
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*dst = append(*dst, repo)
}

func sameConfiguredRepoHost(left, right string) bool {
	if left == "" {
		left = "github.com"
	}
	if right == "" {
		right = "github.com"
	}
	return strings.EqualFold(left, right)
}
