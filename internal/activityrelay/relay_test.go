package activityrelay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscribe opens a stream and returns hints as they arrive. The stream is
// closed when the test ends.
func subscribe(t *testing.T, feed *httptest.Server) <-chan Hint {
	t.Helper()
	require := require.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := Open(ctx, feed.Client(), feed.URL)
	require.NoError(err)
	hints := make(chan Hint, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = stream.Read(func(hint Hint) { hints <- hint })
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = stream.Close()
	})
	return hints
}

func TestSignedWebhookFansOutAndKeepsPayloadsPrivate(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	assert := assert.New(t)
	secret := []byte("synthetic-signing-secret")
	feed := new(Broadcaster)
	ingress, private := Handlers(feed, map[string]Source{"team": {Secret: secret, RepositoryIDs: []int64{12345}}})
	feedServer := httptest.NewServer(private)
	t.Cleanup(feedServer.Close)
	first := subscribe(t, feedServer)
	second := subscribe(t, feedServer)
	body := `{"action":"edited","repository":{"id":12345,"node_id":"R_test_project","full_name":"private-repository-marker"},"pull_request":{"number":42,"title":"private-title-marker","body":"private-body-marker"},"sender":{"login":"private-user-marker"}}`
	response := deliver(t, ingress, secret, "pull_request", body)
	assert.Equal(http.StatusNoContent, response.Code)
	want := Hint{Provider: "github", Host: "github.com", RepositoryID: "R_test_project", Target: PullRequest, Number: 42}
	assert.Equal(want, <-first)
	assert.Equal(want, <-second)
	bad := deliver(t, ingress, []byte("wrong-secret"), "pull_request", body)
	assert.Equal(http.StatusUnauthorized, bad.Code)
	assert.NotContains(bad.Body.String(), "private-")
	unlisted := deliver(t, ingress, secret, "pull_request", strings.ReplaceAll(body, "12345", "99999"))
	assert.Equal(http.StatusBadRequest, unlisted.Code)
	assert.NotContains(unlisted.Body.String(), "private-")
	select {
	case hint := <-first:
		require.FailNow("rejected deliveries must not reach subscribers", "%+v", hint)
	default:
	}
	publicFeed := httptest.NewRecorder()
	ingress.ServeHTTP(publicFeed, httptest.NewRequest(http.MethodGet, "/activity", nil))
	assert.Equal(http.StatusNotFound, publicFeed.Code)
	privateWebhook := deliver(t, private, secret, "pull_request", body)
	assert.Equal(http.StatusNotFound, privateWebhook.Code)
	health := httptest.NewRecorder()
	private.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(http.StatusNoContent, health.Code)
}

func TestSubscriberReleaseAndSlowConsumers(t *testing.T) {
	assert := assert.New(t)
	t.Parallel()
	feed := new(Broadcaster)
	hints, cancel := feed.Subscribe()
	assert.Equal(1, feed.Subscribers())
	hint := Hint{Provider: "github", Host: "github.com", RepositoryID: "R_test_project", Target: Repository}
	for range subscriberBuffer + 5 {
		feed.Publish([]Hint{hint})
	}
	assert.Len(hints, subscriberBuffer, "a stalled subscriber drops hints instead of blocking the webhook")
	cancel()
	assert.Zero(feed.Subscribers())
	feed.Publish([]Hint{hint})
	assert.Len(hints, subscriberBuffer)
}

func TestOpenRejectsNonStreamResponses(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/activity":
			_, _ = io.WriteString(w, `{"events":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	_, err := Open(t.Context(), server.Client(), server.URL)
	require.Error(err, "a page response must not be mistaken for a subscription")
	_, err = Open(t.Context(), server.Client(), server.URL+"/missing")
	require.Error(err)
}

func TestReadRejectsInvalidHints(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	stream := &Stream{body: io.NopCloser(strings.NewReader(": connected\n\nevent: hint\ndata: {\"provider\":\"github\",\"host\":\"github.com\",\"repository_id\":\"R_x\",\"target\":\"issue\",\"number\":0}\n\n"))}
	var received []Hint
	err := stream.Read(func(hint Hint) { received = append(received, hint) })
	require.Error(err)
	require.Empty(received)
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
		{"checks", "check_run", `"check_run":{"pull_requests":[{"number":7}]}`, "", 0, 204},
		{"suite", "check_suite", `"check_suite":{"pull_requests":[{"number":7}]}`, "", 0, 204},
		{"workflow", "workflow_run", `"workflow_run":{"pull_requests":[{"number":7}]}`, "", 0, 204},
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
			feed := new(Broadcaster)
			hints, cancel := feed.Subscribe()
			t.Cleanup(cancel)
			secret := []byte("synthetic-secret")
			ingress, _ := Handlers(feed, map[string]Source{"team": {Secret: secret, RepositoryIDs: []int64{12345}}})
			response := deliver(t, ingress, secret, tt.event, `{"action":"created","repository":{"id":12345,"node_id":"R_test_project"},`+tt.fields+`}`)
			require.Equal(tt.status, response.Code, response.Body.String())
			if tt.target == "" {
				require.Empty(hints)
				return
			}
			require.Len(hints, 1)
			hint := <-hints
			assert.Equal(tt.target, hint.Target)
			assert.Equal(tt.number, hint.Number)
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
