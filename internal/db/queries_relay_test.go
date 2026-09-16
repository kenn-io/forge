package db_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/activityrelay"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestRelayCheckpointAndPendingWork(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	assert := assert.New(t)
	path := filepath.Join(t.TempDir(), "forge.db")
	database := dbtest.OpenWithMigrationsAt(t, path)
	ctx := t.Context()
	const relayURL = "https://relay.example.com"
	ci := activityrelay.Hint{Provider: "github", Host: "github.com", RepositoryID: "R_test_project", Target: activityrelay.PullRequestChecks, Number: 42}
	pr := ci
	pr.Target = activityrelay.PullRequest
	require.NoError(database.SaveRelayPage(ctx, relayURL, "checkpoint-a", []activityrelay.Hint{ci, pr, ci}))
	work, err := database.PendingRelayHints(ctx, relayURL)
	require.NoError(err)
	assert.Equal([]activityrelay.Hint{pr}, work)
	invalid := ci
	invalid.Target = "invalid"
	err = database.SaveRelayPage(ctx, relayURL, "checkpoint-b", []activityrelay.Hint{ci, invalid})
	require.Error(err)
	cursor, err := database.RelayCursor(ctx, relayURL)
	require.NoError(err)
	assert.Equal("checkpoint-a", cursor)
	require.NoError(database.Close())
	reopened := dbtest.OpenPreparedAt(t, path)
	work, err = reopened.PendingRelayHints(ctx, relayURL)
	require.NoError(err)
	assert.Equal([]activityrelay.Hint{pr}, work)
	require.NoError(reopened.CompleteRelayHint(ctx, relayURL, pr))
	work, err = reopened.PendingRelayHints(ctx, relayURL)
	require.NoError(err)
	assert.Empty(work)
	cursor, err = reopened.RelayCursor(ctx, "https://another.example.com")
	require.NoError(err)
	assert.Empty(cursor)
}
