package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
)

func TestUntrackedRepoOpenListAfterTrackedFilter(t *testing.T) {
	srv, database, _ := setupTestServer(t)
	_, err := database.WriteDB().ExecContext(t.Context(), `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < 12)
		INSERT INTO untracked_repo_opens SELECT 'github', 'github.com', 'other', 'repo-' || x, 1, printf('2020-01-01T00:00:%02dZ', x) FROM n`)
	require.NoError(t, err)
	_, err = database.WriteDB().ExecContext(t.Context(), `INSERT INTO untracked_repo_opens VALUES ('github', 'github.com', 'acme', 'widget', 1, '2030-01-01T00:00:00Z')`)
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/repos/untracked-opens", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var rows []db.UntrackedRepoOpen
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 12)
	require.Equal(t, "repo-12", rows[0].Name)
	require.Equal(t, "repo-1", rows[11].Name)
}

func TestUntrackedRepoOpenLogFields(t *testing.T) { //nolint:paralleltest // swaps the default logger
	srv, database, _ := setupTestServer(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/repo/github/acme/other/resolve/42?item_type=issue", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"repo_tracked":false`)
	rows, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[0].Count)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	require.Equal(t, "opened untracked repository", entry["msg"])
	require.Equal(t, "github", entry["provider"])
	require.Equal(t, "github.com", entry["host"])
	require.Equal(t, "acme", entry["owner"])
	require.Equal(t, "other", entry["name"])
	require.Equal(t, "issue", entry["item_type"])
	require.EqualValues(t, 42, entry["number"])
}

func TestUntrackedRepoOpenAPI(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		require.Less(t, rr.Code, 300, rr.Body.String())
		return rr
	}
	body := `{"provider":"github","host":"github.com","owner":"other","name":"repo","item_type":"issue","number":77}`
	invalid := httptest.NewRequest(http.MethodPost, "/api/v1/repos/untracked-opens", strings.NewReader(`{"provider":"github","host":"github.com","owner":"bad/owner","name":"repo"}`))
	invalid.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	srv.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code)
	request(http.MethodPost, "/api/v1/repos/untracked-opens", body)
	request(http.MethodPost, "/api/v1/repos/untracked-opens", body)
	request(http.MethodPost, "/api/v1/repos/untracked-opens", `{"provider":"github","host":"github.com","owner":"acme","name":"widget"}`)
	request(http.MethodPost, "/api/v1/repo/github/another/repo/resolve/12", "")
	rr := request(http.MethodGet, "/api/v1/repos/untracked-opens", "")
	var rows []db.UntrackedRepoOpen
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 2)
	require.Equal(t, "other", rows[0].Owner)
	require.Equal(t, 2, rows[0].Count)
	require.Equal(t, "another", rows[1].Owner)
	require.Equal(t, 1, rows[1].Count)
	recorded, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, recorded, 2)
	syncer.SetRepos(append(serverfake.DefaultTestRepos, ghclient.RepoRef{Platform: "github", PlatformHost: "github.com", Owner: "other", Name: "repo"}))
	rr = request(http.MethodGet, "/api/v1/repos/untracked-opens", "")
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "another", rows[0].Owner)
}

func TestUntrackedRepoOpenValidationAndTracking(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	for _, body := range []string{
		`{"provider":"unknown","host":"example.com","owner":"group","name":"project"}`,
		`{"provider":"github","host":"bad host","owner":"group","name":"project"}`,
		`{"provider":"github","host":"github.com","owner":"bad/owner","name":"project"}`,
		`{"provider":"github","host":"github.com","owner":"group","name":"bad/name"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/untracked-opens", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	post := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/untracked-opens", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		require.Less(t, rr.Code, 300, rr.Body.String())
	}
	post(`{"provider":"gitlab","host":"gitlab.com","owner":"group/subgroup","name":"project"}`)
	post(`{"provider":"github","host":"github.com","owner":"acme","name":"widget"}`)
	rows, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "group/subgroup", rows[0].Owner)
	syncer.SetRepos(nil)
	post(`{"provider":"github","host":"github.com","owner":"acme","name":"widget"}`)
	rows, err = database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestUntrackedSyncCountsUserRequestsButNotIssuePolling(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	syncer.SetRepos(nil)
	for _, tc := range []struct {
		path  string
		count int
	}{
		{"/api/v1/pulls/gh/acme/widget/7/sync", 1},
		{"/api/v1/issues/gh/acme/widget/7/sync?if_changed=true", 1},
		{"/api/v1/issues/gh/acme/widget/7/sync", 2},
	} {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, tc.path, nil))
		require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		rows, err := database.ListUntrackedRepoOpens(t.Context())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, tc.count, rows[0].Count)
	}
}

func TestResolveGitLabRecordsUntrackedSync(t *testing.T) {
	database := dbtest.Open(t)
	_, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", Key: platform.RepositoryIDKey(99), Owner: "group", Name: "project"})
	require.NoError(t, err)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	h := itemapi.Handlers{Db: database, Syncer: &syncer, RepoResolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}), IsConfiguredRepoTracked: func(db.Repo) bool { return true }}
	_, err = h.ResolveItem(t.Context(), &itemapi.ResolveItemInput{Provider: "gitlab", PlatformHost: "gitlab.com", Owner: "group", Name: "project", Number: 7, ItemType: "pr"})
	var problem *httpapi.ProblemError
	require.ErrorAs(t, err, &problem)
	require.Equal(t, http.StatusForbidden, problem.Status)
	rows, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[0].Count)
}

func TestUntrackedOpenListOmitsResolvedGlob(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	require.NoError(t, database.RecordUntrackedRepoOpen(t.Context(), db.UntrackedRepoOpen{Provider: "github", Host: "github.com", Owner: "acme", Name: "tools"}))
	syncer.SetRepos([]ghclient.RepoRef{{Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "tools", ConfiguredRepoPath: "acme/*"}})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/repos/untracked-opens", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.JSONEq(t, `[]`, rr.Body.String())
}

func TestUntrackedOpenLogsWhenCounterWriteFails(t *testing.T) { //nolint:paralleltest // swaps the default logger
	database := dbtest.Open(t)
	_, err := database.WriteDB().ExecContext(t.Context(), `DROP TABLE untracked_repo_opens`)
	require.NoError(t, err)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	err = httpapi.RecordUntrackedRepoOpen(t.Context(), database, "github", "github.com", "acme", "other", "issue", 42)
	require.Error(t, err)
	var entry map[string]any
	require.NoError(t, json.NewDecoder(&logs).Decode(&entry))
	require.Equal(t, "opened untracked repository", entry["msg"])
	require.Equal(t, "acme", entry["owner"])
	require.Equal(t, "other", entry["name"])
	require.Equal(t, "issue", entry["item_type"])
	require.EqualValues(t, 42, entry["number"])
}
