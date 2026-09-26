package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

	ghclient "go.kenn.io/forge/internal/github"
)

func notificationsEnabledConfig() *config.Config {
	// Notifications are always on; a non-nil config is all the server
	// needs to serve the notification APIs.
	return &config.Config{}
}

func TestToNotificationResponseRejectsBlankProvider(t *testing.T) {
	require := require.New(t)
	s := wiredServer(&Server{})
	_, err := s.notificationapi.ToNotificationResponse(t.Context(), db.Notification{
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-42",
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Review requested",
		Reason:                 "mention",
		Unread:                 true,
		Participating:          true,
		SourceUpdatedAt:        time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		SyncedAt:               time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
	}, map[int64]*db.Repo{})
	require.ErrorContains(err, "notification provider is required")
}

func TestNotificationsAPIExposesBackgroundSyncStatus(t *testing.T) {
	require := require.New(t)
	database := serverfake.OpenTestDB(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com": &serverfake.MockGH{
				ListNotificationsFn: func(context.Context, ghclient.NotificationListOptions) ([]ghclient.NotificationThread, bool, error) {
					return nil, false, errors.New("notification API unavailable")
				},
			},
		},
		database, nil,
		[]ghclient.RepoRef{{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	s := New(database, syncer, nil, "/", notificationsEnabledConfig(), ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/notifications/sync", nil)
	require.NoError(err)
	respReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusAccepted, resp.StatusCode)

	require.Eventually(func() bool {
		respReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/notifications?state=all", nil)
		require.NoError(err)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var body struct {
			Sync struct {
				Running   bool   `json:"running"`
				LastError string `json:"last_error"`
			} `json:"sync"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false
		}
		return !body.Sync.Running && strings.Contains(body.Sync.LastError, "notification API unavailable")
	}, 2*time.Second, 20*time.Millisecond)
}

func TestGlobalSyncExposesNotificationSyncFailure(t *testing.T) {
	require := require.New(t)
	database := serverfake.OpenTestDB(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com": &serverfake.MockGH{
				ListNotificationsFn: func(context.Context, ghclient.NotificationListOptions) ([]ghclient.NotificationThread, bool, error) {
					return nil, false, errors.New("notification API unavailable")
				},
			},
		},
		database, nil,
		[]ghclient.RepoRef{{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	s := New(database, syncer, nil, "/", notificationsEnabledConfig(), ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, s) })
	ts := httptest.NewServer(s)
	defer ts.Close()

	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/sync", nil)
	require.NoError(err)
	respReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusAccepted, resp.StatusCode)
	require.Eventually(func() bool {
		respReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/notifications?state=all", nil)
		require.NoError(err)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var body struct {
			Sync struct {
				Running   bool   `json:"running"`
				LastError string `json:"last_error"`
			} `json:"sync"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false
		}
		// The global repo sync intentionally overlaps notification sync and
		// can win both identity-reconciliation attempts. The notification-only
		// test above pins the provider error; this test pins global-path status.
		return !body.Sync.Running && body.Sync.LastError != ""
	}, 15*time.Second, 20*time.Millisecond)
}
