package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
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

func TestUntrackedRepoOpenValidationAndTracking(t *testing.T) {
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

func TestUntrackedSyncCountsUserRequestsButNotIssuePolling(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	ctx := t.Context()
	repoID, err := reposeed.Seed(ctx, database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = database.UpsertIssue(ctx, &db.Issue{RepoID: repoID, PlatformID: 101, Number: 7, URL: "https://github.com/acme/widget/issues/7", Title: "Issue", Author: "user", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(t, err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{RepoID: repoID, PlatformID: 102, Number: 8, URL: "https://github.com/acme/widget/pull/8", Title: "Pull", Author: "user", State: "open", HeadBranch: "feature", BaseBranch: "main", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(t, err)
	syncer.SetRepos(nil)
	for _, tc := range []struct {
		path  string
		count int
	}{
		{"/api/v1/pulls/gh/acme/widget/9/sync", 1},
		{"/api/v1/issues/gh/acme/widget/9/sync?if_changed=true", 1},
		{"/api/v1/issues/gh/acme/widget/9/sync", 2},
	} {
		rr := testutil.DoJSON(t, srv, http.MethodPost, tc.path, nil)
		require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		rows, err := database.ListUntrackedRepoOpens(t.Context())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, tc.count, rows[0].Count)
	}
	for _, path := range []string{"/api/v1/issues/gh/acme/widget/7/sync", "/api/v1/pulls/gh/acme/widget/8/sync"} {
		rr := testutil.DoJSON(t, srv, http.MethodPost, path, nil)
		require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		rows, err := database.ListUntrackedRepoOpens(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, 2, rows[0].Count)
	}
}

func TestDirectDetailLinksRecordUntrackedRepos(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	for _, tc := range []struct {
		path  string
		count int
	}{
		{"/api/v1/pulls/gh/missing/repo/4", 1},
		{"/api/v1/issues/gh/missing/repo/5", 2},
		{"/api/v1/host/github.com/pulls/gh/missing/repo/4", 3},
		{"/api/v1/host/github.com/issues/gh/missing/repo/5", 4},
	} {
		rr := testutil.DoJSON(t, srv, http.MethodGet, tc.path, nil)
		require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		require.Contains(t, rr.Body.String(), `"code":"repoNotFound"`)
		rows, err := database.ListUntrackedRepoOpens(t.Context())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, tc.count, rows[0].Count)
	}
	syncer.SetRepos(nil)
	for _, tc := range []struct{ path, code string }{
		{"/api/v1/pulls/gh/acme/widget/99999", "pullNotFound"},
		{"/api/v1/issues/gh/acme/widget/99999", "issueNotFound"},
	} {
		rr := testutil.DoJSON(t, srv, http.MethodGet, tc.path, nil)
		require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		require.Contains(t, rr.Body.String(), `"code":"`+tc.code+`"`)
	}
	rows, err := database.ListUntrackedRepoOpens(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 4, rows[0].Count)
}

func TestTrackedRepoWithoutCacheDoesNotRecordOpen(t *testing.T) {
	srv, database, syncer := setupTestServer(t)
	syncer.SetRepos([]ghclient.RepoRef{{Platform: "github", PlatformHost: "github.com", Owner: "new", Name: "repo"}})
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/v1/pulls/gh/new/repo/4", http.StatusNotFound},
		{http.MethodGet, "/api/v1/issues/gh/new/repo/5", http.StatusNotFound},
		{http.MethodPost, "/api/v1/repo/github/new/repo/resolve/6", http.StatusOK},
	} {
		rr := testutil.DoJSON(t, srv, tc.method, tc.path, nil)
		require.Equal(t, tc.status, rr.Code, rr.Body.String())
		rows, err := database.ListUntrackedRepoOpens(t.Context())
		require.NoError(t, err)
		require.Empty(t, rows, tc.path)
	}
}
