package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestUntrackedOpenLogsWhenCounterWriteFails(t *testing.T) { //nolint:paralleltest // swaps the default logger
	database := dbtest.Open(t)
	_, err := database.WriteDB().ExecContext(t.Context(), `DROP TABLE untracked_repo_opens`)
	require.NoError(t, err)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	err = RecordUntrackedRepoOpen(t.Context(), database, "github", "github.com", "acme", "other", "issue", 42)
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
