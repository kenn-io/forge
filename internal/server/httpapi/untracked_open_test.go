package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

func TestUntrackedOpenLogsWhenCounterWriteFails(t *testing.T) { //nolint:paralleltest // swaps the default logger
	database := dbtest.Open(t)
	_, err := database.WriteDB().ExecContext(t.Context(), `DROP TABLE untracked_repo_opens`)
	require.NoError(t, err)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	err = RecordUntrackedRepoOpen(t.Context(), database, syncer, "github", "github.com", "acme", "other", "issue", 42)
	require.Error(t, err)
	var entry map[string]any
	decoder := json.NewDecoder(&logs)
	require.NoError(t, decoder.Decode(&entry))
	require.NoError(t, decoder.Decode(&entry))
	require.Equal(t, "WARN", entry["level"])
	require.Equal(t, "record untracked repository open", entry["msg"])
	require.NotEmpty(t, entry["error"])
}

func TestUntrackedOpenFiltersTrackedProviderOnSameHost(t *testing.T) {
	for _, tc := range []struct {
		tracked  platform.Kind
		opened   string
		recorded bool
	}{
		{platform.KindGitHub, "gitlab", true},
		{platform.KindGitLab, "github", true},
		{platform.KindGitHub, "github", false},
	} {
		t.Run(string(tc.tracked)+"/"+tc.opened, func(t *testing.T) {
			database := dbtest.Open(t)
			syncer := ghclient.NewSyncer(nil, database, nil, []ghclient.RepoRef{{Platform: tc.tracked, PlatformHost: "code.example.com", Owner: "group", Name: "project"}}, time.Minute, nil, nil)
			t.Cleanup(syncer.Stop)
			require.NoError(t, RecordUntrackedRepoOpen(t.Context(), database, syncer, tc.opened, "code.example.com", "group", "project", "issue", 1))
			rows, err := database.ListUntrackedRepoOpens(t.Context())
			require.NoError(t, err)
			if tc.recorded {
				require.Len(t, rows, 1)
				require.Equal(t, tc.opened, rows[0].Provider)
			} else {
				require.Empty(t, rows)
			}
		})
	}
}
