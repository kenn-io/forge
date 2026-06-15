package fleet

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchemaVersionAndRawRoundTrip(t *testing.T) {
	require := require.New(t)
	require.GreaterOrEqual(SchemaVersion, 1, "SchemaVersion must be >= 1")
	raw := RawSnapshot{
		SchemaVersion: SchemaVersion,
		Host:          RawHost{Hostname: "studio", Platform: "linux"},
		Projects:      []RawProject{{ScopedKey: "repo:/srv/app", Name: "app", RootPath: "/srv/app"}},
		Worktrees:     []RawWorktree{{ScopedKey: "worktree:/srv/app", ProjectKey: "repo:/srv/app", Path: "/srv/app", IsPrimary: true}},
		Sessions:      []RawSession{{ScopedKey: "session:app-main", WorktreeKey: "worktree:/srv/app", Status: "running"}},
	}
	b, err := json.Marshal(raw)
	require.NoError(err)
	var back RawSnapshot
	require.NoError(json.Unmarshal(b, &back))
	require.Equal(SchemaVersion, back.SchemaVersion, "round-trip schema version")
	require.Len(back.Projects, 1, "round-trip projects")
}
