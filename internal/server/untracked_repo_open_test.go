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
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestUntrackedRepoOpenListAfterTrackedFilter(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	_, err := database.WriteDB().ExecContext(t.Context(), `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < 12)
		INSERT INTO untracked_repo_opens SELECT 'github', 'github.com', 'other', 'repo-' || x, 1, printf('2020-01-01T00:00:%02dZ', x) FROM n`)
	require.NoError(t, err)
	_, err = database.WriteDB().ExecContext(t.Context(), `INSERT INTO untracked_repo_opens VALUES ('github', 'github.com', 'acme', 'widget', 1, '2030-01-01T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, database.RecordUntrackedRepoOpen(t.Context(), db.UntrackedRepoOpen{Provider: "github", Host: "github.com", Owner: "acme", Name: "tools"}))
	syncer.SetRepos([]ghclient.RepoRef{
		{Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget"},
		{Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "tools", ConfiguredRepoPath: "acme/*"},
	})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repos/untracked-opens", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var rows []db.UntrackedRepoOpen
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 12)
}

func TestUntrackedRepoOpenLogFields(t *testing.T) { //nolint:paralleltest // swaps the default logger
	srv, database, _ := setupTestServer(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/repo/github/acme/other/resolve/42?item_type=issue", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"repo_tracked":false`)
	rows, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[0].Count)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	require.Equal(t, "issue", entry["item_type"])
	require.EqualValues(t, 42, entry["number"])
}

func TestUntrackedRepoOpenAPI(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		require.Less(t, rr.Code, 300, rr.Body.String())
		return rr
	}
	body := `{"provider":"github","host":"github.com","owner":"other","name":"repo","item_type":"issue","number":77}`
	request(http.MethodPost, "/api/v1/repos/untracked-opens", body)
	request(http.MethodPost, "/api/v1/repos/untracked-opens", body)
	rr := request(http.MethodGet, "/api/v1/repos/untracked-opens", "")
	var rows []db.UntrackedRepoOpen
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "other", rows[0].Owner)
	require.Equal(t, 2, rows[0].Count)
}

func TestUntrackedRepoOpenValidationAndTracking(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	for _, body := range []string{
		`{"provider":"unknown","host":"example.com","owner":"group","name":"project"}`,
		`{"provider":"github","host":"bad host","owner":"group","name":"project"}`,
		`{"provider":"github","host":"github.com","owner":"bad/owner","name":"project"}`,
		`{"provider":"github","host":"github.com","owner":"group","name":"bad/name"}`,
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/repos/untracked-opens", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	post := func(body string) {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/repos/untracked-opens", strings.NewReader(body))
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
		srv.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodPost, tc.path, nil))
		require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		rows, err := database.ListUntrackedRepoOpens(t.Context())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, tc.count, rows[0].Count)
	}
}

func TestDirectDetailLinksRecordUntrackedRepos(t *testing.T) {
	srv, database, _ := setupTestServer(t)
	for _, name := range []string{"one", "two", "three"} {
		require.NoError(t, database.RecordUntrackedRepoOpen(t.Context(), db.UntrackedRepoOpen{Provider: "github", Host: "github.com", Owner: "other", Name: name}))
	}
	for _, tc := range []struct {
		path  string
		count int
	}{
		{"/api/v1/pulls/gh/missing/repo/4", 1},
		{"/api/v1/issues/gh/missing/repo/5", 2},
		{"/api/v1/host/github.com/pulls/gh/missing/repo/4", 3},
		{"/api/v1/host/github.com/issues/gh/missing/repo/5", 4},
	} {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
		require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		require.Contains(t, rr.Body.String(), `"code":"repoNotFound"`)
		rows, err := database.ListUntrackedRepoOpens(t.Context())
		require.NoError(t, err)
		require.Len(t, rows, 4)
		counts := map[string]int{}
		for _, row := range rows {
			counts[row.Owner+"/"+row.Name] = row.Count
		}
		require.Equal(t, map[string]int{"other/one": 1, "other/two": 1, "other/three": 1, "missing/repo": tc.count}, counts)
	}
	for _, tc := range []struct{ path, code string }{
		{"/api/v1/pulls/gh/acme/widget/99999", "pullNotFound"},
		{"/api/v1/issues/gh/acme/widget/99999", "issueNotFound"},
	} {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
		require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		require.Contains(t, rr.Body.String(), `"code":"`+tc.code+`"`)
	}
	rows, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 4)
	require.Equal(t, 4, rows[0].Count)
}

func TestCachedUntrackedDetailSyncDoesNotCountAgain(t *testing.T) {
	srv, database, _, syncer := setupTestServerWithConfig(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = database.UpsertIssue(ctx, &db.Issue{RepoID: repoID, PlatformID: 101, Number: 7, URL: "https://github.com/acme/widget/issues/7", Title: "Issue", Author: "user", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(t, err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{RepoID: repoID, PlatformID: 102, Number: 8, URL: "https://github.com/acme/widget/pull/8", Title: "Pull", Author: "user", State: "open", HeadBranch: "feature", BaseBranch: "main", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(t, err)
	syncer.SetRepos(nil)
	for _, tc := range []struct{ detail, sync, kind string }{
		{"/api/v1/issues/gh/acme/widget/7", "/api/v1/issues/gh/acme/widget/7/sync", "issue"},
		{"/api/v1/pulls/gh/acme/widget/8", "/api/v1/pulls/gh/acme/widget/8/sync", "pr"},
	} {
		rr := testutil.DoJSON(t, srv, http.MethodGet, tc.detail, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos/untracked-opens", map[string]any{"provider": "github", "host": "github.com", "owner": "acme", "name": "widget", "item_type": tc.kind})
		require.Less(t, rr.Code, 300, rr.Body.String())
		testutil.DoJSON(t, srv, http.MethodPost, tc.sync, nil)
		testutil.DoJSON(t, srv, http.MethodGet, tc.detail, nil)
	}
	rows, err := database.ListUntrackedRepoOpens(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].Count)
}
