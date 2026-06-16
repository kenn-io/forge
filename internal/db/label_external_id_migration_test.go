package db

import (
	"path/filepath"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"
)

// Migration 000031 clears the platform_external_id values that GitLab
// item-label normalization used to derive from label names. Without the
// cleanup, the first catalog refresh after upgrading can match a legacy
// assigned label named like another label's decimal ID and rewire the
// assignment to the wrong catalog label.
func TestOpenClearsGitLabNameDerivedLabelExternalIDs(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "upgrade.db")

	d, err := Open(path)
	require.NoError(err)

	gitlabRepoID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)
	giteaRepoID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.com",
		Owner:        "acme",
		Name:         "gadget",
		RepoPath:     "acme/gadget",
	})
	require.NoError(err)

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	insertLabel := func(repoID int64, name, externalID string) {
		_, err := d.WriteDB().ExecContext(ctx, `
			INSERT INTO middleman_labels (repo_id, platform_external_id, name, updated_at)
			VALUES (?, ?, ?, ?)`,
			repoID, externalID, name, now,
		)
		require.NoError(err)
	}
	// Legacy pre-upgrade GitLab rows claimed the name as external ID.
	insertLabel(gitlabRepoID, "4", "4")
	insertLabel(gitlabRepoID, "bug", "bug")
	// Catalog rows already keyed by decimal label IDs stay untouched.
	insertLabel(gitlabRepoID, "triage", "17")
	// Non-GitLab repos legitimately key labels by decimal IDs that can
	// equal the label name; the cleanup must not touch them.
	insertLabel(giteaRepoID, "12", "12")

	// Rewind to the pre-cleanup schema version and reopen so migration
	// 000031 runs against the seeded legacy rows. The migration is
	// data-only, but later migrations are not: drop the schema that
	// 000032-000034 add so the replay applies cleanly.
	require.NoError(removeMergeRequestUserListColumnsForTest(d.WriteDB()))
	require.NoError(removeEventDirectURLColumnsForTest(d.WriteDB()))
	require.NoError(removeProjectDiscoveryColumnsForTest(d.WriteDB()))
	_, err = d.WriteDB().ExecContext(ctx, `UPDATE schema_migrations SET version = 30, dirty = 0`)
	require.NoError(err)
	d.Close()

	upgraded, err := Open(path)
	require.NoError(err)
	defer upgraded.Close()

	externalID := func(repoID int64, name string) string {
		var id string
		require.NoError(upgraded.ReadDB().QueryRowContext(ctx, `
			SELECT platform_external_id FROM middleman_labels
			WHERE repo_id = ? AND name = ?`,
			repoID, name,
		).Scan(&id))
		return id
	}
	assert.Empty(externalID(gitlabRepoID, "4"), "name-derived ID colliding with a decimal ID must be cleared")
	assert.Empty(externalID(gitlabRepoID, "bug"), "name-derived ID must be cleared")
	assert.Equal("17", externalID(gitlabRepoID, "triage"), "decimal catalog IDs must be preserved")
	assert.Equal("12", externalID(giteaRepoID, "12"), "non-GitLab labels named like their ID must be preserved")
}
