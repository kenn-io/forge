package github

import (
	"context"
	"log/slog"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"
)

// Display-name cache parameters. Display names rarely change,
// so the success TTL is long enough to skip lookups across many
// sync passes; failures use a shorter TTL so a transient 404
// does not suppress a real retry for hours. The size bound is
// well above any realistic author set for a fixed repo list.
const (
	displayNameCacheSize  = 1024
	displayNameSuccessTTL = 24 * time.Hour
	displayNameFailureTTL = 15 * time.Minute
)

// resolveDisplayName returns the GitHub display name for a
// login and whether the lookup succeeded. Returns ("", false)
// on API failure so callers can preserve existing data. Uses a
// TTL + LRU cache that spans the Syncer's lifetime plus
// singleflight dedup so concurrent workers racing on the same
// author only trigger one GetUser call. When a refetch fails
// but a stale cache entry exists, the stale value is returned
// (stale-while-error).
//
// Bot logins (ending with "[bot]") are returned as-is since bot
// accounts have no display name on the GitHub API.
//
// The lookup is routed by repo so a configuration with no host
// fallback route still resolves names, and so the request is
// billed to the identity serving that repository. A display name
// does not vary by credential, so the cache stays host-keyed.
func (s *Syncer) resolveDisplayName(
	ctx context.Context, client Client, repo RepoRef, login string,
) (string, bool) {
	key := repoHost(repo) + "\x00" + login
	if cached, fresh := s.displayNames.get(key); fresh {
		return cached.name, cached.ok
	}
	if strings.HasSuffix(login, "[bot]") {
		s.displayNames.putSuccess(key, login)
		return login, true
	}

	v, err, _ := s.displayNameGroup.Do(key, func() (any, error) {
		// Re-check the cache inside the singleflight slot:
		// another caller may have populated a fresh entry
		// while this one was waiting for its turn to run.
		if cached, fresh := s.displayNames.get(key); fresh {
			return cached, nil
		}
		user, err := s.getUserForRepo(ctx, client, repo, login)
		if err != nil {
			return displayNameEntry{}, err
		}
		name := nameOrEmpty(user)
		s.displayNames.putSuccess(key, name)
		return displayNameEntry{name: name, ok: true}, nil
	})
	if err != nil {
		// Fall back to a stale cached name if one exists so a
		// transient network error does not blank out an
		// already-known name. A zero entry has ok=false, so a
		// total miss falls through to the failure path below.
		//
		// Also back off the retry window: re-use the stored
		// name but with failureTTL so repeated failures do not
		// hit /users every sync for the life of successTTL.
		if stale, _ := s.displayNames.get(key); stale.ok {
			s.displayNames.putStaleFallback(key, stale.name)
			return stale.name, true
		}
		logSyncFailure(ctx, slog.LevelWarn, "get user display name failed", err,
			"login", login,
		)
		s.displayNames.putFailure(key)
		return "", false
	}
	result := v.(displayNameEntry)
	return result.name, result.ok
}

// getUserForRepo prefers a repository-routed user lookup so routed hosts pick
// the repository's credential. A client without repository routing, or a repo
// ref missing owner/name, falls back to the plain host lookup.
func (s *Syncer) getUserForRepo(
	ctx context.Context, client Client, repo RepoRef, login string,
) (*gh.User, error) {
	if reader, ok := client.(repoUserClient); ok &&
		repo.Owner != "" && repo.Name != "" {
		return reader.GetUserForRepo(ctx, repo.Owner, repo.Name, login)
	}
	return client.GetUser(ctx, login)
}
