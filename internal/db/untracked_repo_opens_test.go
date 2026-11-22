package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUntrackedRepoOpens(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "forge.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	first := UntrackedRepoOpen{Provider: "github", Host: "github.com", Owner: "Acme", Name: "repo"}
	require.NoError(t, d.RecordUntrackedRepoOpen(t.Context(), first))
	first.Owner = "acme"
	require.NoError(t, d.RecordUntrackedRepoOpen(t.Context(), first))
	other := UntrackedRepoOpen{Provider: "gitlab", Host: "gitlab.example.com", Owner: "acme", Name: "repo"}
	require.NoError(t, d.RecordUntrackedRepoOpen(t.Context(), other))
	rows, err := d.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "github", rows[0].Provider)
	require.Equal(t, 2, rows[0].Count)
	require.Equal(t, "gitlab", rows[1].Provider)
	require.Equal(t, 1, rows[1].Count)
	require.Regexp(t, `^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`, rows[1].LastOpenedAt)
	require.NoError(t, d.RecordUntrackedRepoOpen(t.Context(), other))
	_, err = d.WriteDB().ExecContext(t.Context(), `UPDATE untracked_repo_opens SET last_opened_at = '2020-01-01T00:00:00.000Z' WHERE provider = 'gitlab'`)
	require.NoError(t, err)
	rows, err = d.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "github", rows[0].Provider)
	require.Equal(t, "gitlab", rows[1].Provider)
}

func TestUntrackedRepoOpensRemainAvailable(t *testing.T) {
	d := openTestDB(t)
	_, err := d.WriteDB().ExecContext(t.Context(), `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < 250)
		INSERT INTO untracked_repo_opens SELECT 'github', 'github.com', 'acme', 'repo-' || x, 1, '2020-01-01T00:00:00Z' FROM n`)
	require.NoError(t, err)
	require.NoError(t, d.RecordUntrackedRepoOpen(t.Context(), UntrackedRepoOpen{Provider: "github", Host: "github.com", Owner: "new", Name: "repo"}))
	rows, err := d.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 251)
	require.Equal(t, "new", rows[0].Owner)
	require.Regexp(t, `^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`, rows[0].LastOpenedAt)
}
