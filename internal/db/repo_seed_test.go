package db

import (
	"context"
	"hash/fnv"
	"strings"
)

// seedTestRepo records identity as an observed repository and returns its row
// ID, like reposeed.Seed (which this package cannot import). A route-only
// identity gets syntheticTestRepoID for that route, so seeding the same route
// twice returns the same row.
func seedTestRepo(ctx context.Context, d *DB, identity RepoIdentity) (int64, error) {
	identity = canonicalRepoIdentity(identity)
	if identity.PlatformRepoID == 0 {
		identity.PlatformRepoID = syntheticTestRepoID(identity)
	}
	entry, err := d.ObserveRepository(ctx, identity)
	if err != nil {
		return 0, err
	}
	return entry.Repository.ID, nil
}

// syntheticTestRepoID is a stable provider repository ID derived from a
// route, matching reposeed.SyntheticID.
func syntheticTestRepoID(identity RepoIdentity) int64 {
	identity = canonicalRepoIdentity(identity)
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(identity.Platform + "\x00" + identity.PlatformHost +
		"\x00" + strings.ToLower(identity.RepoPath)))
	return 1_000_000 + int64(hash.Sum32())
}
