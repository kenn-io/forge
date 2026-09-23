package activitytest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server"
)

func seedServerNotification(t *testing.T, database *db.DB) int64 {
	t.Helper()
	require := require.New(t)
	repoID, err := database.UpsertRepo(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	number := 42
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-42",
		RepoID:                 &repoID,
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Review requested",
		WebURL:                 "https://github.com/acme/widget/pull/42",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "octocat",
		Reason:                 "review_requested",
		Unread:                 true,
		Participating:          true,
		SourceUpdatedAt:        now,
		SyncedAt:               now,
	}}))
	items, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)
	return items[0].ID
}

func TestNotificationsAPIRejectsNilConfigAccess(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	id := seedServerNotification(t, database)
	s := server.New(database, nil, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	body, err := json.Marshal(map[string]any{"ids": []int64{id}})
	require.NoError(err)
	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/notifications/read", bytes.NewReader(body))
	require.NoError(err)
	respReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusForbidden, resp.StatusCode)
}
