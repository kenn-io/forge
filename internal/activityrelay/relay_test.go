package activityrelay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

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
	body := `{"action":"edited","repository":{"id":12345,"full_name":"private-repository-marker"},"pull_request":{"number":42,"title":"private-title-marker","body":"private-body-marker"},"sender":{"login":"private-user-marker"}}`
	response := deliver(t, ingress, secret, "pull_request", body)
	assert.Equal(http.StatusNoContent, response.Code)
	want := Hint{Provider: "github", Host: "github.com", RepositoryID: 12345, Target: PullRequest, Number: 42}
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
	ingress.ServeHTTP(publicFeed, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/activity", nil))
	assert.Equal(http.StatusNotFound, publicFeed.Code)
	privateWebhook := deliver(t, private, secret, "pull_request", body)
	assert.Equal(http.StatusNotFound, privateWebhook.Code)
	health := httptest.NewRecorder()
	private.ServeHTTP(health, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
	assert.Equal(http.StatusNoContent, health.Code)
}

func TestSubscriberReleaseAndSlowConsumers(t *testing.T) {
	assert := assert.New(t)
	t.Parallel()
	feed := new(Broadcaster)
	hints, cancel := feed.Subscribe()
	assert.Equal(1, feed.Subscribers())
	hint := Hint{Provider: "github", Host: "github.com", RepositoryID: 12345, Target: Repository}
	for range cap(hints) + 5 {
		feed.Publish([]Hint{hint})
	}
	assert.Len(hints, cap(hints), "a stalled subscriber drops hints instead of blocking the webhook")
	cancel()
	assert.Zero(feed.Subscribers())
	feed.Publish([]Hint{hint})
	assert.Len(hints, cap(hints))
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

// blockedWriter behaves like a socket whose peer stopped reading: a write
// blocks until the deadline in force has expired.
type blockedWriter struct {
	header    http.Header
	mu        sync.Mutex
	deadlines []time.Time
	changed   chan struct{}
	started   chan struct{}
	once      sync.Once
	// holdExpiry, when set, keeps the caller inside an expired-deadline
	// install until the channel closes, modelling a callback still using the
	// ResponseWriter.
	holdExpiry chan struct{}
}

func (w *blockedWriter) Header() http.Header { return w.header }
func (w *blockedWriter) WriteHeader(int)     {}
func (w *blockedWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	for !w.expired() {
		<-w.changed
	}
	return 0, errors.New("write deadline exceeded")
}

func (w *blockedWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	select {
	case w.changed <- struct{}{}:
	default:
	}
	if w.holdExpiry != nil && !deadline.IsZero() && !deadline.After(time.Now()) {
		<-w.holdExpiry
	}
	return nil
}

func (w *blockedWriter) lastDeadline() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deadlines[len(w.deadlines)-1]
}

func (w *blockedWriter) expired() bool {
	deadline := w.lastDeadline()
	return !deadline.IsZero() && !deadline.After(time.Now())
}

func awaitSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		require.FailNow(t, message)
	}
}

func TestShutdownReleasesASubscriberBlockedInWrite(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	writer := &blockedWriter{header: http.Header{}, changed: make(chan struct{}, 1), started: make(chan struct{})}
	feed := new(Broadcaster)
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveStream(ctx, writer, feed)
	}()
	// Hints waiting in the subscriber's buffer at shutdown must not re-arm a
	// fresh write deadline after cancellation expired the previous one.
	feed.Publish([]Hint{{Provider: "github", Host: "github.com", RepositoryID: 1001, Target: Repository}})
	awaitSignal(t, writer.started, "the stream did not start writing")
	require.True(writer.lastDeadline().After(time.Now()), "a frame write carries a bounded deadline")
	cancel()
	awaitSignal(t, done, "cancellation must interrupt a write that is blocked on a stalled subscriber")
	require.False(writer.lastDeadline().After(time.Now()), "the expired deadline must be the one left in force")
}

func TestShutdownWaitsForAStartedExpiryCallback(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		ctx, cancel := context.WithCancel(t.Context())
		writer := &blockedWriter{header: http.Header{}, changed: make(chan struct{}, 1), started: make(chan struct{}), holdExpiry: make(chan struct{})}
		feed := new(Broadcaster)
		done := make(chan struct{})
		go func() {
			defer close(done)
			serveStream(ctx, writer, feed)
		}()
		feed.Publish([]Hint{{Provider: "github", Host: "github.com", RepositoryID: 1001, Target: Repository}})
		<-writer.started
		cancel()
		// The expiry callback wakes the blocked write, then stays inside its
		// deadline install. Once every goroutine is blocked, the handler must
		// still be waiting for that callback rather than returned.
		synctest.Wait()
		select {
		case <-done:
			require.FailNow("the handler returned while the expiry callback was still using the ResponseWriter")
		default:
		}
		close(writer.holdExpiry)
		<-done
	})
}

func TestReadReconnectsWhenAnOpenStreamStalls(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": connected\n\n")
		http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	stream, err := Open(t.Context(), server.Client(), server.URL)
	require.NoError(err)
	t.Cleanup(func() { _ = stream.Close() })
	stream.idleTimeout = 50 * time.Millisecond
	err = stream.Read(func(Hint) {})
	require.ErrorContains(err, "stalled", "silence past the keepalive interval must end the subscription")
}

func TestReadRejectsInvalidHints(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	stream := &Stream{idleTimeout: time.Second, body: io.NopCloser(strings.NewReader(": connected\n\nevent: hint\ndata: {\"provider\":\"github\",\"host\":\"github.com\",\"repository_id\":1001,\"target\":\"issue\",\"number\":0}\n\n"))}
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
		{"pending workflow", "workflow_run", `"workflow_run":{"pull_requests":[{"number":7}]}`, "", 0, 204},
		{"unassociated workflow", "workflow_run", `"workflow_run":{"pull_requests":[]}`, "", 0, 204},
		{"missing workflow", "workflow_run", `"extra":true`, "", 0, 400},
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
			t.Cleanup(feed.Close)
			hints, cancel := feed.Subscribe()
			t.Cleanup(cancel)
			secret := []byte("synthetic-secret")
			ingress, _ := Handlers(feed, map[string]Source{"team": {Secret: secret, RepositoryIDs: []int64{12345}}})
			response := deliver(t, ingress, secret, tt.event, `{"action":"created","repository":{"id":12345},`+tt.fields+`}`)
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

func TestWorkflowRunHintsBatchBeforeReachingSubscribers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		feed := new(Broadcaster)
		defer feed.Close()
		first, cancelFirst := feed.Subscribe()
		defer cancelFirst()
		second, cancelSecond := feed.Subscribe()
		defer cancelSecond()
		secret := []byte("synthetic-secret")
		ingress, _ := Handlers(feed, map[string]Source{"team": {Secret: secret, RepositoryIDs: []int64{12345}}})
		for i, action := range []string{"requested", "in_progress", "completed"} {
			response := deliver(t, ingress, secret, "workflow_run", `{"action":"`+action+`","repository":{"id":12345},"workflow_run":{"name":"Private workflow","head_sha":"private-sha","pull_requests":[{"number":7},{"number":8}]}}`)
			require.Equal(http.StatusNoContent, response.Code, response.Body.String())
			if i == 0 {
				time.Sleep(59 * time.Second)
			}
		}
		synctest.Wait()
		assert.Empty(first, "workflow bursts stay in the relay")
		assert.Empty(second)
		response := deliver(t, ingress, secret, "pull_request", `{"repository":{"id":12345},"pull_request":{"number":7}}`)
		require.Equal(http.StatusNoContent, response.Code)
		ordinary := Hint{Provider: "github", Host: "github.com", RepositoryID: 12345, Target: PullRequest, Number: 7}
		require.Equal(ordinary, <-first, "ordinary updates do not wait behind checks")
		require.Equal(ordinary, <-second)
		// Individual check updates share the workflow's existing window.
		response = deliver(t, ingress, secret, "check_run", `{"repository":{"id":12345},"check_run":{"pull_requests":[{"number":7},{"number":9}]}}`)
		require.Equal(http.StatusNoContent, response.Code)
		time.Sleep(time.Second)
		synctest.Wait()
		check := ordinary
		check.Target = PullRequestChecks
		other := check
		other.Number = 8
		checkOnly := check
		checkOnly.Number = 9
		for _, hints := range []<-chan Hint{first, second} {
			runs := ordinary
			runs.Target, runs.Number = "workflow_runs", 0
			require.Len(hints, 4, "one hint per PR and one for the repository's runs")
			assert.ElementsMatch([]Hint{check, other, checkOnly, runs}, []Hint{<-hints, <-hints, <-hints, <-hints})
		}
		feed.Publish([]Hint{check})
		time.Sleep(59 * time.Second)
		feed.Publish([]Hint{check})
		assert.Empty(first)
		time.Sleep(time.Second)
		synctest.Wait()
		require.Len(first, 1)
		require.Len(second, 1)
		assert.Equal(check, <-first)
		assert.Equal(check, <-second)
		feed.Publish([]Hint{check})
		feed.Close()
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Empty(first, "shutdown discards pending checks")
	})
}

func TestUnassociatedActionsHints(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		feed := new(Broadcaster)
		defer feed.Close()
		hints, cancel := feed.Subscribe()
		defer cancel()
		secret := []byte("synthetic-secret")
		ingress, _ := Handlers(feed, map[string]Source{"team": {Secret: secret, RepositoryIDs: []int64{12345}}})
		for _, event := range []string{"check_run", "workflow_run"} {
			response := deliver(t, ingress, secret, event, `{"repository":{"id":12345},"`+event+`":{"pull_requests":[]}}`)
			require.Equal(http.StatusNoContent, response.Code)
		}
		require.Empty(hints)
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Len(hints, 1, "a run without a PR refreshes Actions, never every PR")
		assert.Equal(t, Hint{Provider: "github", Host: "github.com", RepositoryID: 12345, Target: "workflow_runs"}, <-hints)
	})
}

func TestPendingChecksDoNotCrowdOutActivity(t *testing.T) {
	t.Parallel()
	feed := new(Broadcaster)
	defer feed.Close()
	hints, cancel := feed.Subscribe()
	defer cancel()
	for number := 1; number <= 1025; number++ {
		feed.Publish([]Hint{{Provider: "github", Host: "github.com", RepositoryID: 1001, Target: PullRequestChecks, Number: number}})
	}
	for _, target := range []string{PullRequest, Issue, RepositoryRefs, Repository} {
		hint := Hint{Provider: "github", Host: "github.com", RepositoryID: 1001, Target: target, Number: 1}
		feed.Publish([]Hint{hint})
		require.Equal(t, hint, <-hints, "pending checks must not occupy ordinary activity buffers")
	}
}

func TestFlushedChecksLeaveRoomForWorkflowAndActivity(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		batches int
	}{
		{name: "full batch", batches: 1},
		{name: "backlogged subscriber", batches: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				feed := new(Broadcaster)
				defer feed.Close()
				hints, cancel := feed.Subscribe()
				defer cancel()
				checks := make([]Hint, 1024)
				for i := range checks {
					checks[i] = Hint{Provider: "github", Host: "github.com", RepositoryID: 1001, Target: PullRequestChecks, Number: i + 1}
				}
				workflow := Hint{Provider: "github", Host: "github.com", RepositoryID: 1001, Target: WorkflowRuns}
				for i := range tt.batches {
					feed.Publish(checks)
					if i == tt.batches-1 {
						feed.Publish([]Hint{workflow})
					}
					time.Sleep(time.Minute)
					synctest.Wait()
				}
				ordinary := Hint{Provider: "github", Host: "github.com", RepositoryID: 1001, Target: PullRequest, Number: 1}
				feed.Publish([]Hint{ordinary})
				require.Len(t, hints, len(checks)+2, "checks must leave room for workflow updates and immediate activity")
				received := make([]Hint, len(checks)+1)
				for i := range received {
					received[i] = <-hints
				}
				assert.ElementsMatch(t, slices.Concat(checks, []Hint{workflow}), received)
				assert.Equal(t, ordinary, <-hints)
			})
		})
	}
}

func deliver(t *testing.T, handler http.Handler, secret []byte, event, body string) *httptest.ResponseRecorder {
	t.Helper()
	require := require.New(t)
	mac := hmac.New(sha256.New, secret)
	_, err := io.WriteString(mac, body)
	require.NoError(err)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/github/team", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}
