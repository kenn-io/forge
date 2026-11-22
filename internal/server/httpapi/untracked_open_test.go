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
	require.NoError(t, json.NewDecoder(&logs).Decode(&entry))
	require.Equal(t, "opened untracked repository", entry["msg"])
	require.Equal(t, "github", entry["provider"])
	require.Equal(t, "github.com", entry["host"])
	require.Equal(t, "acme", entry["owner"])
	require.Equal(t, "other", entry["name"])
	require.Equal(t, "issue", entry["item_type"])
	require.EqualValues(t, 42, entry["number"])
}

func TestUntrackedOpenDistinguishesProvidersOnSameHost(t *testing.T) {
	for _, tc := range []struct {
		tracked platform.Kind
		opened  string
	}{
		{platform.KindGitHub, "gitlab"},
		{platform.KindGitLab, "github"},
	} {
		t.Run(tc.opened, func(t *testing.T) {
			database := dbtest.Open(t)
			syncer := ghclient.NewSyncer(nil, database, nil, []ghclient.RepoRef{{Platform: tc.tracked, PlatformHost: "code.example.com", Owner: "group", Name: "project"}}, time.Minute, nil, nil)
			t.Cleanup(syncer.Stop)
			require.NoError(t, RecordUntrackedRepoOpen(t.Context(), database, syncer, tc.opened, "code.example.com", "group", "project", "issue", 1))
			rows, err := database.ListUntrackedRepoOpens(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, tc.opened, rows[0].Provider)
		})
	}
}
