package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil"
)

func TestUntrackedRepoOpenListAfterTrackedFilter(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	require.NoError(t, database.RecordUntrackedRepoOpen(t.Context(), db.UntrackedRepoOpen{Provider: "github", Host: "github.com", Owner: "other", Name: "repo"}))
	require.NoError(t, database.RecordUntrackedRepoOpen(t.Context(), db.UntrackedRepoOpen{Provider: "github", Host: "github.com", Owner: "acme", Name: "widget"}))
	require.NoError(t, database.RecordUntrackedRepoOpen(t.Context(), db.UntrackedRepoOpen{Provider: "github", Host: "github.com", Owner: "acme", Name: "tools"}))
	syncer.SetRepos([]ghclient.RepoRef{
		{Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget"},
		{Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "tools"},
	})
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos/untracked-opens", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var rows []db.UntrackedRepoOpen
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "other", rows[0].Owner)
}

func TestUntrackedRepoOpenLogFields(t *testing.T) { //nolint:paralleltest // swaps the default logger
	srv, database, _ := setupTestServer(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repo/github/acme/other/resolve/42?item_type=issue", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"repo_tracked":false`)
	rows, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[0].Count)
	var entry map[string]any
	require.NoError(t, json.NewDecoder(&logs).Decode(&entry))
	require.Equal(t, "issue", entry["item_type"])
	require.EqualValues(t, 42, entry["number"])
}

func TestUntrackedRepoOpenValidation(t *testing.T) {
	srv, database, _ := setupTestServer(t)
	for _, body := range []string{
		`{"provider":"github","host":"github.com","owner":"group","name":"project","item_type":"bogus"}`,
		`{"provider":"github","host":"github.com","owner":"group","name":"project","number":-1}`,
		`{"provider":"unknown","host":"example.com","owner":"group","name":"project"}`,
	} {
		rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/untracked-opens", json.RawMessage(body))
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	post := func(body string) {
		t.Helper()
		rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/untracked-opens", json.RawMessage(body))
		require.Less(t, rr.Code, 300, rr.Body.String())
	}
	post(`{"provider":"gitlab","host":"gitlab.com","owner":"group/subgroup","name":"project"}`)
	rows, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "group/subgroup", rows[0].Owner)
}

func TestResolveCachedUntrackedRepoRecordsOpen(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	syncer.SetRepos(nil)
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repo/github/acme/widget/resolve/9?item_type=issue", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"repo_tracked":false`)
	rows, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "widget", rows[0].Name)
	require.Equal(t, 1, rows[0].Count)
}

func TestDetailReadsDoNotRecordUntrackedRepos(t *testing.T) {
	srv, database, _ := setupTestServer(t)
	for _, path := range []string{
		"/api/v1/pulls/gh/missing/repo/4",
		"/api/v1/issues/gh/missing/repo/5",
		"/api/v1/host/github.com/pulls/gh/missing/repo/4",
		"/api/v1/host/github.com/issues/gh/missing/repo/5",
	} {
		rr := testutil.DoJSON(t, srv, http.MethodGet, path, nil)
		require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		require.Contains(t, rr.Body.String(), `"code":"repoNotFound"`)
		rows, err := database.ListUntrackedRepoOpens(t.Context())
		require.NoError(t, err)
		require.Empty(t, rows)
	}
}
