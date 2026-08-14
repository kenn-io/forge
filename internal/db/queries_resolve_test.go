package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveItemNumber(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, verifiedTestRepoIdentity("github", "github.com", "acme", "widget"))
	require.NoError(err)

	// Seed a PR at number 10
	insertTestMRWithOptions(t, database, testMR(repoID, 10, withMRTitle("PR ten")))

	// Seed an issue at number 20
	insertTestIssueWithOptions(t, database, testIssue(repoID, 20, withIssueTitle("Issue twenty")))

	// Resolve PR
	itemType, found, err := database.ResolveItemNumber(ctx, repoID, 10)
	require.NoError(err)
	assert.True(found)
	assert.Equal("pr", itemType)

	// Resolve issue
	itemType, found, err = database.ResolveItemNumber(ctx, repoID, 20)
	require.NoError(err)
	assert.True(found)
	assert.Equal("issue", itemType)

	// Unknown number
	_, found, err = database.ResolveItemNumber(ctx, repoID, 999)
	require.NoError(err)
	assert.False(found)

	// Typed resolution avoids PR precedence for providers whose issue
	// and merge request number spaces can overlap.
	insertTestIssueWithOptions(t, database, testIssue(repoID, 10, withIssueTitle("Issue ten")))
	itemType, found, err = database.ResolveItemNumberOfType(ctx, repoID, 10, "issue")
	require.NoError(err)
	assert.True(found)
	assert.Equal("issue", itemType)

	itemType, found, err = database.ResolveItemNumberOfType(ctx, repoID, 20, "pr")
	require.NoError(err)
	assert.False(found)
	assert.Empty(itemType)
}

func TestResolveItemNumberHidesOnlyRemovedUpstreamItems(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, database, "acme", "widget")
	now := time.Now().UTC().Truncate(time.Second)

	insertTestMR(t, database, repoID, 1, "inaccessible pull", now)
	insertTestMR(t, database, repoID, 2, "removed pull", now)
	insertTestIssue(t, database, repoID, 3, "inaccessible issue", now)
	insertTestIssue(t, database, repoID, 4, "removed issue", now)
	_, err := database.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES
			(?, 'merge_request', 1, 'pr-1', ?, ?, 'inaccessible'),
			(?, 'merge_request', 2, 'pr-2', ?, ?, 'removed_upstream'),
			(?, 'issue', 3, 'issue-3', ?, ?, 'inaccessible'),
			(?, 'issue', 4, 'issue-4', ?, ?, 'removed_upstream')`,
		repoID, now, now, repoID, now, now,
		repoID, now, now, repoID, now, now,
	)
	require.NoError(err)

	for _, tc := range []struct {
		number int
		kind   string
		found  bool
	}{
		{number: 1, kind: "pr", found: true},
		{number: 2, found: false},
		{number: 3, kind: "issue", found: true},
		{number: 4, found: false},
	} {
		kind, found, resolveErr := database.ResolveItemNumber(ctx, repoID, tc.number)
		require.NoError(resolveErr)
		require.Equal(tc.found, found)
		require.Equal(tc.kind, kind)
	}

	for _, tc := range []struct {
		number int
		kind   string
		found  bool
	}{
		{number: 1, kind: "pr", found: true},
		{number: 2, kind: "pr", found: false},
		{number: 3, kind: "issue", found: true},
		{number: 4, kind: "issue", found: false},
	} {
		kind, found, resolveErr := database.ResolveItemNumberOfType(ctx, repoID, tc.number, tc.kind)
		require.NoError(resolveErr)
		require.Equal(tc.found, found)
		if tc.found {
			require.Equal(tc.kind, kind)
		} else {
			require.Empty(kind)
		}
	}
}
