package activityrelay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeedReplayAndRetention(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	assert := assert.New(t)
	path := filepath.Join(t.TempDir(), "relay.db")
	store, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(store.Close()) })
	head, err := store.Read(t.Context(), "", 100)
	require.NoError(err)
	require.True(head.ResyncRequired)
	hint := Hint{Provider: "github", Host: "github.com", RepositoryID: "R_test_project", Target: "pull_request", Number: 42}
	require.NoError(store.Append(t.Context(), []Hint{hint, hint}))
	page, err := store.Read(t.Context(), head.NextCursor, 1)
	require.NoError(err)
	require.Len(page.Events, 1)
	assert.Equal(hint, page.Events[0].Hint)
	assert.True(page.HasMore)
	require.NoError(store.Close())
	store, err = Open(path)
	require.NoError(err)
	replayed, err := store.Read(t.Context(), head.NextCursor, 1)
	require.NoError(err)
	assert.Equal(page, replayed)
	last, err := store.Read(t.Context(), page.NextCursor, 1)
	require.NoError(err)
	require.Len(last.Events, 1)
	assert.False(last.HasMore)
	empty, err := store.Read(t.Context(), last.NextCursor, 100)
	require.NoError(err)
	assert.Empty(empty.Events)
	assert.Equal(last.NextCursor, empty.NextCursor)
	require.NoError(store.Prune(t.Context(), time.Now().Add(time.Hour)))
	expired, err := store.Read(t.Context(), head.NextCursor, 100)
	require.NoError(err)
	assert.Equal("cursor_expired", expired.Code)
	assert.Equal(last.NextCursor, expired.ResetCursor)
	require.NoError(store.Append(t.Context(), []Hint{hint}))
	newPage, err := store.Read(t.Context(), expired.ResetCursor, 100)
	require.NoError(err)
	require.Len(newPage.Events, 1)
	assert.NotEqual(last.NextCursor, newPage.NextCursor)
	other, err := Open(filepath.Join(t.TempDir(), "replacement.db"))
	require.NoError(err)
	t.Cleanup(func() { require.NoError(other.Close()) })
	reset, err := other.Read(t.Context(), newPage.NextCursor, 100)
	require.NoError(err)
	assert.Equal("cursor_expired", reset.Code)
	_, err = store.Read(t.Context(), "invalid", 100)
	require.Error(err)
}

func TestSignedWebhookReductionAndPrivacy(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	assert := assert.New(t)
	path := filepath.Join(t.TempDir(), "relay.db")
	store, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(store.Close()) })
	secret := []byte("synthetic-signing-secret")
	ingress, feed := Handlers(store, map[string]Source{"team": {Secret: secret, RepositoryIDs: []int64{12345}}})
	feedServer := httptest.NewServer(feed)
	t.Cleanup(feedServer.Close)
	head, err := Fetch(t.Context(), feedServer.Client(), feedServer.URL, "")
	require.NoError(err)
	body := `{"action":"edited","repository":{"id":12345,"node_id":"R_test_project","full_name":"private-repository-marker"},"pull_request":{"number":42,"title":"private-title-marker","body":"private-body-marker"},"sender":{"login":"private-user-marker"}}`
	response := deliver(t, ingress, secret, "pull_request", body)
	assert.Equal(http.StatusNoContent, response.Code)
	bad := deliver(t, ingress, []byte("wrong-secret"), "pull_request", body)
	assert.Equal(http.StatusUnauthorized, bad.Code)
	assert.NotContains(bad.Body.String(), "private-")
	unlisted := deliver(t, ingress, secret, "pull_request", strings.ReplaceAll(body, "12345", "99999"))
	assert.Equal(http.StatusBadRequest, unlisted.Code)
	assert.NotContains(unlisted.Body.String(), "private-")
	page, err := Fetch(t.Context(), feedServer.Client(), feedServer.URL, head.NextCursor)
	require.NoError(err)
	require.Len(page.Events, 1)
	assert.Equal(Hint{Provider: "github", Host: "github.com", RepositoryID: "R_test_project", Target: "pull_request", Number: 42}, page.Events[0].Hint)
	for _, suffix := range []string{"", "-wal"} {
		data, readErr := os.ReadFile(path + suffix)
		require.NoError(readErr)
		assert.NotContains(string(data), "private-")
		assert.NotContains(string(data), string(secret))
	}
	publicFeed := httptest.NewRecorder()
	ingress.ServeHTTP(publicFeed, httptest.NewRequest(http.MethodGet, "/activity", nil))
	assert.Equal(http.StatusNotFound, publicFeed.Code)
	privateWebhook := deliver(t, feed, secret, "pull_request", body)
	assert.Equal(http.StatusNotFound, privateWebhook.Code)
}

func TestWebhookTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, event, fields, target string
		number, status              int
	}{
		{"pull", "pull_request", `"pull_request":{"number":7}`, "pull_request", 7, 204},
		{"review", "pull_request_review", `"pull_request":{"number":7}`, "pull_request", 7, 204},
		{"thread", "pull_request_review_thread", `"pull_request":{"number":7}`, "pull_request", 7, 204},
		{"review comment", "pull_request_review_comment", `"pull_request":{"number":7}`, "pull_request", 7, 204},
		{"issue", "issues", `"issue":{"number":8}`, "issue", 8, 204},
		{"issue comment", "issue_comment", `"issue":{"number":8}`, "issue", 8, 204},
		{"pull comment", "issue_comment", `"issue":{"number":7,"pull_request":{}}`, "pull_request", 7, 204},
		{"checks", "check_run", `"check_run":{"pull_requests":[{"number":7}]}`, "pull_request_checks", 7, 204},
		{"suite", "check_suite", `"check_suite":{"pull_requests":[{"number":7}]}`, "pull_request_checks", 7, 204},
		{"workflow", "workflow_run", `"workflow_run":{"pull_requests":[{"number":7}]}`, "pull_request_checks", 7, 204},
		{"unassociated", "check_run", `"check_run":{"pull_requests":[]}`, "", 0, 204},
		{"push", "push", `"ref":"refs/heads/private-branch"`, "repository_refs", 0, 204},
		{"create", "create", `"ref":"private-branch"`, "repository_refs", 0, 204},
		{"delete", "delete", `"ref":"private-branch"`, "repository_refs", 0, 204},
		{"repository", "repository", `"extra":true`, "repository", 0, 204},
		{"status", "status", `"sha":"private-sha"`, "", 0, 204},
		{"job", "workflow_job", `"workflow_job":{}`, "", 0, 204},
		{"ping", "ping", `"zen":"private-text"`, "", 0, 204},
		{"missing PR", "pull_request", `"extra":true`, "", 0, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			t.Parallel()
			store, err := Open(filepath.Join(t.TempDir(), "relay.db"))
			require.NoError(err)
			t.Cleanup(func() { require.NoError(store.Close()) })
			secret := []byte("synthetic-secret")
			ingress, _ := Handlers(store, map[string]Source{"team": {Secret: secret, RepositoryIDs: []int64{12345}}})
			head, err := store.Read(t.Context(), "", 100)
			require.NoError(err)
			response := deliver(t, ingress, secret, tt.event, `{"action":"created","repository":{"id":12345,"node_id":"R_test_project"},`+tt.fields+`}`)
			require.Equal(tt.status, response.Code, response.Body.String())
			page, err := store.Read(t.Context(), head.NextCursor, 100)
			require.NoError(err)
			if tt.target == "" {
				require.Empty(page.Events)
				return
			}
			require.Len(page.Events, 1)
			assert.Equal(tt.target, page.Events[0].Target)
			assert.Equal(tt.number, page.Events[0].Number)
		})
	}
}

func deliver(t *testing.T, handler http.Handler, secret []byte, event, body string) *httptest.ResponseRecorder {
	require := require.New(t)
	t.Helper()
	mac := hmac.New(sha256.New, secret)
	_, err := io.WriteString(mac, body)
	require.NoError(err)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github/team", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}
