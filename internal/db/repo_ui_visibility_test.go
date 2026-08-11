package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hiddenRepoIDs(t *testing.T, d *DB) []int64 {
	t.Helper()
	hidden, err := d.HiddenRepos(t.Context())
	require.NoError(t, err)
	ids := make([]int64, 0, len(hidden))
	for _, repo := range hidden {
		ids = append(ids, repo.ID)
	}
	return ids
}

func TestSetRepoHiddenFromUIRoundTrip(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	otherID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "gadget"))
	require.NoError(err)

	assert.Empty(hiddenRepoIDs(t, d))

	require.NoError(d.SetRepoHiddenFromUI(ctx, repoID, true))
	assert.Equal([]int64{repoID}, hiddenRepoIDs(t, d))

	hidden, err := d.HiddenRepos(ctx)
	require.NoError(err)
	require.Len(hidden, 1)
	assert.Equal("acme", hidden[0].Owner)
	assert.Equal("widget", hidden[0].Name)

	// Hiding again and hiding a second repo are both allowed.
	require.NoError(d.SetRepoHiddenFromUI(ctx, repoID, true))
	require.NoError(d.SetRepoHiddenFromUI(ctx, otherID, true))
	assert.ElementsMatch([]int64{repoID, otherID}, hiddenRepoIDs(t, d))

	require.NoError(d.SetRepoHiddenFromUI(ctx, repoID, false))
	assert.Equal([]int64{otherID}, hiddenRepoIDs(t, d))

	// Showing an already-visible repo is a no-op.
	require.NoError(d.SetRepoHiddenFromUI(ctx, repoID, false))
	assert.Equal([]int64{otherID}, hiddenRepoIDs(t, d))
}

func TestSetRepoHiddenFromUIUnknownRepo(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	require.Error(d.SetRepoHiddenFromUI(ctx, 12345, true))

	// Clearing a preference for an unknown repo has nothing to remove.
	require.NoError(d.SetRepoHiddenFromUI(ctx, 12345, false))
}

// TestSetRepoHiddenFromUIUnderReadLockExcludesDisplacement pins the locking
// contract the visibility mutation relies on: while a caller holds the
// repository-reconciliation read lock, a displacing reconciliation must wait,
// so resolving the row's lifecycle and writing the preference cannot
// interleave with the displacement.
func TestSetRepoHiddenFromUIUnderReadLockExcludesDisplacement(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	observedAt := time.Now().UTC()
	oldIdentity := GitHubRepoIdentity("github.com", "acme", "widget")
	oldIdentity.PlatformRepoID = "R_old"
	entry, _, err := d.ReconcileRepositoryObservation(ctx, oldIdentity, observedAt)
	require.NoError(err)
	require.NotNil(entry)

	release, err := d.LockRepositoryReconciliationRead(ctx)
	require.NoError(err)

	// A different repository takes over the route while the lock is held.
	displaced := make(chan error, 1)
	go func() {
		newIdentity := GitHubRepoIdentity("github.com", "acme", "widget")
		newIdentity.PlatformRepoID = "R_new"
		_, _, err := d.ReconcileRepositoryObservation(
			ctx, newIdentity, observedAt.Add(time.Second),
		)
		displaced <- err
	}()

	select {
	case err := <-displaced:
		release()
		require.Failf("displacement ran despite the read lock", "err: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	resolved, err := d.GetRepositoryByProviderIDUnderRepositoryReconciliationRead(
		ctx, "github", "github.com", "R_old",
	)
	require.NoError(err)
	require.NotNil(resolved)
	require.Equal(RepositoryLifecycleActive, resolved.Lifecycle,
		"the row stays active for the whole critical section")
	require.NoError(d.SetRepoHiddenFromUI(ctx, resolved.Repository.ID, true))
	release()

	require.NoError(<-displaced)
	after, err := d.GetRepositoryByProviderID(ctx, "github", "github.com", "R_old")
	require.NoError(err)
	require.NotNil(after)
	assert.Equal(RepositoryLifecycleInactive, after.Lifecycle,
		"displacement completed only after the critical section")
	assert.Equal([]int64{resolved.Repository.ID}, hiddenRepoIDs(t, d))
}

func TestHiddenRepoPreferenceCascadesOnRepoDelete(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(d.SetRepoHiddenFromUI(ctx, repoID, true))

	_, err = d.WriteDB().Exec("DELETE FROM forge_repos WHERE id = ?", repoID)
	require.NoError(err)

	assert.Empty(hiddenRepoIDs(t, d))
}
