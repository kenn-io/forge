package github

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"go.kenn.io/forge/platform"
)

func (s *Syncer) trackedRepoOnHost(owner, name, host string) (RepoRef, bool) {
	if host == "" {
		host = "github.com"
	}
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, r := range s.repos {
		rHost := repoHost(r)
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) &&
			strings.EqualFold(rHost, host) {
			return r, true
		}
	}
	return RepoRef{}, false
}

func (s *Syncer) trackedRepo(owner, name string) (RepoRef, bool, error) {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()

	var matched RepoRef
	count := 0
	for _, r := range s.repos {
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) {
			matched = r
			count++
		}
	}
	if count == 0 {
		return RepoRef{}, false, nil
	}
	if count > 1 {
		return RepoRef{}, false, fmt.Errorf(
			"repo %s/%s is ambiguous across configured providers",
			owner, name,
		)
	}
	return matched, true, nil
}

func (s *Syncer) trackedRepoOnHostUnique(
	owner, name, host string,
) (RepoRef, bool, error) {
	if host == "" {
		host = "github.com"
	}
	s.reposMu.Lock()
	defer s.reposMu.Unlock()

	var matched RepoRef
	count := 0
	for _, r := range s.repos {
		rHost := repoHost(r)
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) &&
			strings.EqualFold(rHost, host) {
			matched = r
			count++
		}
	}
	if count == 0 {
		return RepoRef{}, false, nil
	}
	if count > 1 {
		return RepoRef{}, false, fmt.Errorf(
			"repo %s/%s on %s is ambiguous across configured providers",
			owner, name, host,
		)
	}
	return matched, true, nil
}

func (s *Syncer) trackedRepoByIdentity(
	kind platform.Kind,
	owner, name, host string,
) (RepoRef, bool) {
	if kind == "" {
		kind = platform.KindGitHub
	}
	host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, r := range s.repos {
		rHost := repoHost(r)
		if repoPlatform(r) == kind &&
			strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) &&
			strings.EqualFold(rHost, host) {
			return r, true
		}
	}
	return RepoRef{}, false
}

func (s *Syncer) trackedRepoByProviderKey(
	kind platform.Kind,
	host string, key platform.RepositoryKey,
) (RepoRef, bool) {
	if kind == "" {
		kind = platform.KindGitHub
	}
	host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
	if key.IsZero() {
		return RepoRef{}, false
	}
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, repo := range s.repos {
		if repoPlatform(repo) == kind &&
			strings.EqualFold(repoHost(repo), host) &&
			repo.Key == key {
			return repo, true
		}
	}
	return RepoRef{}, false
}

func detailRepoKey(kind platform.Kind, host, owner, name string) string {
	if kind == "" {
		kind = platform.KindGitHub
	}
	host = repoHost(RepoRef{Platform: kind, PlatformHost: host})
	return string(kind) + "\x00" + host + "\x00" +
		strings.ToLower(owner) + "/" + strings.ToLower(name)
}

// hostFor returns the platform host for a repo identified by
// owner/name. Returns "github.com" if not found. Thread-safe.
func (s *Syncer) hostFor(owner, name string) string {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, r := range s.repos {
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) {
			if r.PlatformHost != "" {
				return r.PlatformHost
			}
			return "github.com"
		}
	}
	return "github.com"
}

// HostForRepo returns the platform host for a tracked repo.
// Thread-safe.
func (s *Syncer) HostForRepo(owner, name string) string {
	return s.hostFor(owner, name)
}

// SetRepos atomically replaces the list of repositories to sync.
func (s *Syncer) SetRepos(repos []RepoRef) {
	if err := s.SetReposWithContext(context.Background(), repos, false); err != nil {
		slog.Warn("update archive repositories", "err", err)
		s.reposMu.Lock()
		s.repos = slices.Clone(repos)
		s.reposMu.Unlock()
		s.refreshRepoProblemStatus()
		s.WakeArchive()
	}
}

// SetReposWithContext prepares durable archive state before exposing a new
// configured repository set to sync workers. Credential reloads may also make
// authentication-blocked work eligible without resetting archive progress.
func (s *Syncer) SetReposWithContext(ctx context.Context, repos []RepoRef, retryAuthentication bool) error {
	refs := make([]platform.RepoRef, 0, len(repos))
	for _, repo := range repos {
		refs = append(refs, platformRepoRef(repo))
	}
	if s.AutomaticSyncEnabled() && s.archiveLifecycle != nil {
		seeded, err := s.archiveLifecycle.EnsureConfigured(ctx, refs)
		if err != nil {
			return fmt.Errorf("seed archive discovery: %w", err)
		}
		if retryAuthentication {
			// Only refs that seeded can resolve; a ref skipped by seeding
			// must not fail the retry pass (and with it the config reload).
			if err := s.archiveLifecycle.RetryAuthentication(ctx, seeded); err != nil {
				return fmt.Errorf("retry archive authentication: %w", err)
			}
		}
	}
	s.reposMu.Lock()
	s.repos = slices.Clone(repos)
	s.reposMu.Unlock()
	s.refreshRepoProblemStatus()
	s.WakeArchive()
	return nil
}

// IsTrackedRepo checks whether the given repo is in the configured list.
func (s *Syncer) IsTrackedRepo(owner, name string) bool {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()
	for _, r := range s.repos {
		if strings.EqualFold(r.Owner, owner) &&
			strings.EqualFold(r.Name, name) {
			return true
		}
	}
	return false
}

// TrackedRepos returns a snapshot of the tracked repositories.
func (s *Syncer) TrackedRepos() []RepoRef {
	s.reposMu.Lock()
	defer s.reposMu.Unlock()

	return slices.Clone(s.repos)
}

// isTrackedRepoOnHost checks whether the given repo on a specific host
// is in the configured list. Used by the watched-MR path where the
// host is known and must match exactly.
func (s *Syncer) isTrackedRepoOnHost(owner, name, host string) bool {
	_, ok := s.trackedRepoOnHost(owner, name, host)
	return ok
}

// IsTrackedRepoOnHost checks whether the given repo on a specific host
// is in the configured list.
func (s *Syncer) IsTrackedRepoOnHost(owner, name, host string) bool {
	return s.isTrackedRepoOnHost(owner, name, host)
}
