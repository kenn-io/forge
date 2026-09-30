package db

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMeasureArchiveSnapshotCountsOwnershipJSON guards against the preflight
// text measure silently missing assignees_json/reviewers_json growth: a
// snapshot whose only growth is a long assignee list must still be counted
// toward the 32 MiB export budget before any item body is loaded into Go.
func TestMeasureArchiveSnapshotCountsOwnershipJSON(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	repoID := insertArchiveReportRepo(t, database, RepoIdentity{
		Platform: "github", PlatformHost: "github.example", Owner: "acme", Name: "widget",
	})
	mrID := insertArchiveReportMR(t, database, repoID, 1, "mr-1", "Ownership growth", "author", now)

	baseline := measureArchiveSnapshotText(t, database, repoID, now)

	names := make([]string, 5000)
	for i := range names {
		names[i] = `"user"`
	}
	longAssignees := "[" + strings.Join(names, ",") + "]"
	_, err := database.WriteDB().ExecContext(t.Context(),
		`UPDATE forge_merge_requests SET assignees_json = ? WHERE id = ?`, longAssignees, mrID)
	require.NoError(err)

	grown := measureArchiveSnapshotText(t, database, repoID, now)
	assert.GreaterOrEqual(grown-baseline, int64(len(longAssignees)),
		"assignees_json growth must be counted in the preflight text measure")
}

func measureArchiveSnapshotText(t *testing.T, database *DB, repoID int64, now time.Time) int64 {
	t.Helper()
	tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	measurement, err := MeasureArchiveSnapshot(t.Context(), tx, []int64{repoID}, now.Add(-time.Hour), now.Add(time.Hour), false)
	require.NoError(t, err)
	return measurement.TextBytes
}
