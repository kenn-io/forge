package db

import (
	"testing"

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

	repoID, err := seedTestRepo(ctx, d, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	otherID, err := seedTestRepo(ctx, d, GitHubRepoIdentity("github.com", "acme", "gadget"))
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

func TestHiddenRepoPreferenceCascadesOnRepoDelete(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := seedTestRepo(ctx, d, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(d.SetRepoHiddenFromUI(ctx, repoID, true))

	_, err = d.WriteDB().ExecContext(t.Context(), "DELETE FROM forge_repos WHERE id = ?", repoID)
	require.NoError(err)

	assert.Empty(hiddenRepoIDs(t, d))
}
