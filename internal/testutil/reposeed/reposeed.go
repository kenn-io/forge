// Package reposeed records repositories in test databases the way a provider
// observation would.
package reposeed

import (
	"context"
	"hash/fnv"
	"strings"

	"go.kenn.io/forge/internal/db"
)

// Seed records identity as an observed repository and returns its row ID.
// When identity names only a route, it gets SyntheticID for that route, so
// seeding the same route twice returns the same row.
func Seed(ctx context.Context, d *db.DB, identity db.RepoIdentity) (int64, error) {
	if identity.PlatformRepoID == 0 {
		identity.PlatformRepoID = SyntheticID(identity)
	}
	entry, err := d.ObserveRepository(ctx, identity)
	if err != nil {
		return 0, err
	}
	return entry.Repository.ID, nil
}

// SyntheticID is a stable provider repository ID derived from a route, for
// tests that do not care which ID a repository has.
func SyntheticID(identity db.RepoIdentity) int64 {
	platform := strings.ToLower(strings.TrimSpace(identity.Platform))
	if platform == "" {
		platform = "github"
	}
	host := strings.ToLower(strings.TrimSpace(identity.PlatformHost))
	if host == "" {
		host = "github.com"
	}
	path := strings.Trim(strings.TrimSpace(identity.RepoPath), "/")
	if path == "" {
		path = strings.TrimSpace(identity.Owner) + "/" + strings.TrimSpace(identity.Name)
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(platform + "\x00" + host + "\x00" + strings.ToLower(path)))
	return 1_000_000 + int64(hash.Sum32())
}
