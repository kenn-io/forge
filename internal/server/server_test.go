package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return New(openTestDB(t), nil, nil, "/", nil, ServerOptions{})
}

func TestPreferPtyOwnerForWorkspacesOnWindows(t *testing.T) {
	require := require.New(t)

	prefer := preferPtyOwnerForWorkspaces("windows", true, ServerOptions{
		PtyOwnerManagerPath: "middleman-pty-manager.exe",
	})

	require.True(prefer)
}

func TestHealthzAndLivez_ReturnOK(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()

	for _, path := range []string{"/healthz", "/livez"} {
		resp, err := http.Get(ts.URL + path)
		require.NoError(err)
		assert.Equal(http.StatusOK, resp.StatusCode, path)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var body struct {
			Status string `json:"status"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		require.NoError(err)

		assert.Equal("ok", body.Status, path)
	}
}

func TestHealthz_ReturnsServiceUnavailableAfterDBClose(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := openTestDB(t)
	s := New(database, nil, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	t.Cleanup(func() { gracefulShutdown(t, s) })

	require.NoError(database.Close())

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(err)

	assert.Equal(http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(string(body), "database unavailable")
}

func TestServeRejectsRebindingHost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv := newTestServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()
	t.Cleanup(func() {
		gracefulShutdown(t, srv)
		err := <-errCh
		require.ErrorIs(err, http.ErrServerClosed)
	})

	validResp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	require.NoError(err)
	require.NoError(validResp.Body.Close())
	assert.Equal(http.StatusOK, validResp.StatusCode)

	req, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/healthz", nil)
	require.NoError(err)
	req.Host = "evil.example:8091"

	resp, err := http.DefaultClient.Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	require.Equal(http.StatusForbidden, resp.StatusCode)
	var body struct {
		Error string `json:"error"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(body.Error, "allowed_hosts")
	assert.Contains(body.Error, "trust_reverse_proxy")
}

func TestServeAllowsBoundLoopbackHost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv := newTestServer(t)
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 loopback alias unavailable: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()
	t.Cleanup(func() {
		gracefulShutdown(t, srv)
		err := <-errCh
		require.ErrorIs(err, http.ErrServerClosed)
	})

	validResp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	require.NoError(err)
	require.NoError(validResp.Body.Close())
	assert.Equal(http.StatusOK, validResp.StatusCode)

	req, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/healthz", nil)
	require.NoError(err)
	req.Host = "evil.example:8091"

	resp, err := http.DefaultClient.Do(req)
	require.NoError(err)
	defer resp.Body.Close()
	assert.Equal(http.StatusForbidden, resp.StatusCode)
}

func TestServeHTTPRejectsLoopbackHostFromNonLoopbackPeer(t *testing.T) {
	srv := newTestServer(t)
	srv.allowedHostMu.Lock()
	srv.allowedHosts = map[string]struct{}{
		"127.0.0.1:8091": {},
		"localhost:8091": {},
		"middleman.test": {},
	}
	srv.allowedHostMu.Unlock()

	tests := []struct {
		name       string
		host       string
		remoteAddr string
		want       int
	}{
		{
			name:       "loopback host from loopback client",
			host:       "localhost:8091",
			remoteAddr: "127.0.0.1:4444",
			want:       http.StatusOK,
		},
		{
			name:       "loopback ip host from loopback client",
			host:       "127.0.0.1:8091",
			remoteAddr: "127.0.0.1:4444",
			want:       http.StatusOK,
		},
		{
			name:       "loopback host from remote client",
			host:       "localhost:8091",
			remoteAddr: "192.0.2.10:4444",
			want:       http.StatusForbidden,
		},
		{
			name:       "loopback ip host from remote client",
			host:       "127.0.0.1:8091",
			remoteAddr: "203.0.113.7:4444",
			want:       http.StatusForbidden,
		},
		{
			name:       "allowed non-loopback host from remote client",
			host:       "middleman.test",
			remoteAddr: "192.0.2.10:4444",
			want:       http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)

			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = tt.host
			req.RemoteAddr = tt.remoteAddr
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			require.Equal(tt.want, rec.Code, rec.Body.String())
			if tt.want == http.StatusForbidden {
				var body ProblemError
				require.NoError(json.NewDecoder(rec.Body).Decode(&body))
				assert.Equal(CodeForbidden, body.Code)
				assert.Equal("hostNotAllowed", body.Details["reason"])
			}
		})
	}
}

type staticListenerAddr string

func (a staticListenerAddr) Network() string { return "tcp" }
func (a staticListenerAddr) String() string  { return string(a) }

type staticListener struct {
	addr net.Addr
}

func (l staticListener) Accept() (net.Conn, error) { return nil, errors.New("unused listener") }
func (l staticListener) Close() error              { return nil }
func (l staticListener) Addr() net.Addr            { return l.addr }

func TestAllowedHostsForListenerIncludesBoundLoopbackHost(t *testing.T) {
	assert := assert.New(t)

	allowed := allowedHostsForListener(staticListener{addr: staticListenerAddr("127.0.0.2:8123")})

	assert.Contains(allowed, "127.0.0.2:8123")
	assert.Contains(allowed, "127.0.0.1:8123")
	assert.Contains(allowed, "localhost:8123")
	assert.Contains(allowed, "[::1]:8123")
}

func TestSSE_ReturnsEventStream(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/events")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
}

func TestSSEEndpointE2EFlushesEventsAndCleansUpOnCancel(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)

	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusOK, resp.StatusCode)
	assert.Equal("text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal("no-cache", resp.Header.Get("Cache-Control"))
	require.Eventually(func() bool {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()
		return len(s.hub.subscribers) == 1
	}, 2*time.Second, 10*time.Millisecond)

	s.hub.Broadcast(Event{
		Type: "data_changed",
		Data: map[string]string{"source": "e2e"},
	})

	scanner := bufio.NewScanner(resp.Body)
	var eventType, eventData string
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = rest
		}
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			eventData = rest
		}
		if line == "" && eventType != "" {
			break
		}
	}
	require.Equal("data_changed", eventType)
	assert.JSONEq(`{"source":"e2e"}`, eventData)

	cancel()
	resp.Body.Close()
	require.Eventually(func() bool {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()
		return len(s.hub.subscribers) == 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestSSE_ReceivesBroadcastEvent(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/events")
	require.NoError(t, err)
	defer resp.Body.Close()

	s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})

	scanner := bufio.NewScanner(resp.Body)
	var eventType string
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = rest
		}
		if line == "" && eventType != "" {
			break
		}
	}
	assert.Equal(t, "data_changed", eventType)
}

func TestSSE_InitialSyncStatusFromCache(t *testing.T) {
	s := newTestServer(t)
	s.hub.Broadcast(Event{Type: "sync_status", Data: map[string]bool{"running": false}})

	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/events")
	require.NoError(t, err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var eventType string
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = rest
		}
		if line == "" && eventType != "" {
			break
		}
	}
	assert.Equal(t, "sync_status", eventType)
}

func TestSSE_ExitsCleanlyOnHubClose(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/events")
	require.NoError(t, err)
	defer resp.Body.Close()

	// Close the hub — handler should exit
	s.hub.Close()

	// Read until EOF — should not see zero-value frames
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "event: \ndata:")
}

func TestSSE_MarshalFailureContinuesServing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	// Prime hub so first subscribe gets a sync_status — we read it
	// as proof the handler has subscribed before we broadcast test events.
	s.hub.Broadcast(Event{Type: "sync_status", Data: map[string]bool{"running": false}})

	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/events")
	require.NoError(err)
	defer resp.Body.Close()

	// Single reader goroutine parses SSE frames and sends event types
	// over a channel. Avoids per-read goroutine leaks.
	events := make(chan string, 10)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(resp.Body)
		var evType string
		for scanner.Scan() {
			line := scanner.Text()
			if rest, ok := strings.CutPrefix(line, "event: "); ok {
				evType = rest
			}
			if line == "" && evType != "" {
				events <- evType
				evType = ""
			}
		}
	}()

	// Read initial cached sync_status to confirm subscription is live
	select {
	case ev := <-events:
		assert.Equal("sync_status", ev)
	case <-time.After(5 * time.Second):
		require.FailNow("timed out waiting for initial sync_status")
	}

	// Now safe to broadcast — handler is subscribed
	s.hub.Broadcast(Event{Type: "bad", Data: make(chan int)})
	s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})

	select {
	case ev := <-events:
		assert.Equal("data_changed", ev, "valid event should arrive after marshal failure")
	case <-time.After(5 * time.Second):
		require.FailNow("timed out waiting for data_changed after marshal failure")
	}

	// Close body to unblock reader goroutine, then drain channel.
	// scanner.Err() after forced close returns a read-on-closed-body
	// error — that is expected cleanup, not a test failure. Stream
	// health is validated by successful receipt of both events above.
	resp.Body.Close()
	for range events {
	}
}

func TestSSE_SlowConsumerDisconnect(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/events")
	require.NoError(t, err)
	defer resp.Body.Close()

	// Overrun the buffer without reading — 17 broadcasts (buffer=16)
	for i := range 17 {
		s.hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	// The handler should close the connection
	body := make([]byte, 1)
	_, err = resp.Body.Read(body)
	_ = err // may be EOF or connection reset; we just want to ensure no hang
}

// deadlineControlWriter wraps a ResponseWriter with a controllable
// SetWriteDeadline for testing SSE error paths. failAfter controls how
// many successful calls are allowed before returning an error.
type deadlineControlWriter struct {
	http.ResponseWriter
	mu        sync.Mutex
	failAfter int // calls succeed up to this count; 0 = fail immediately
	calls     int
}

func (w *deadlineControlWriter) SetWriteDeadline(_ time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.calls > w.failAfter {
		return errors.New("deadline not supported")
	}
	return nil
}

// Unwrap lets ResponseController find Flush on the inner writer.
func (w *deadlineControlWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func TestSSE_TerminatesOnInitialDeadlineFailure(t *testing.T) {
	s := newTestServer(t)

	rec := httptest.NewRecorder()
	w := &deadlineControlWriter{ResponseWriter: rec, failAfter: 0}
	r := httptest.NewRequest("GET", "/api/v1/events", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleSSE(w, r)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "handler did not exit on initial deadline failure")
	}
}

func TestSSE_TerminatesOnMidStreamDeadlineFailure(t *testing.T) {
	s := newTestServer(t)
	// Cached sync_status delivered on subscribe triggers mid-stream write
	s.hub.Broadcast(Event{Type: "sync_status", Data: map[string]bool{"running": false}})

	rec := httptest.NewRecorder()
	// First call (initial clear) succeeds; second (pre-write deadline) fails
	w := &deadlineControlWriter{ResponseWriter: rec, failAfter: 1}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := httptest.NewRequest("GET", "/api/v1/events", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleSSE(w, r)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "handler did not exit on mid-stream deadline failure")
	}

	// Cancel context so Subscribe's cleanup goroutine unsubscribes
	cancel()
	require.Eventually(t, func() bool {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()
		return len(s.hub.subscribers) == 0
	}, 2*time.Second, 10*time.Millisecond, "subscriber should be cleaned up after context cancel")

	// Deadline failed before event write — body must be empty
	assert.Empty(t, rec.Body.String(), "no output should be written after deadline failure")
}

// sseFrame is the parsed form of one id:/event:/data: SSE record.
type sseFrame struct {
	ID    string
	Event string
	Data  string
}

// readSSEFrame reads from r until it has one complete frame (terminated
// by a blank line) or the timeout fires. Returns the frame, or fails
// the test if no frame arrives in time.
func readSSEFrame(t *testing.T, scanner *bufio.Scanner) sseFrame {
	t.Helper()
	var f sseFrame
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			f.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			f.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.Data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if f.Event != "" {
				return f
			}
		}
	}
	require.FailNow(t, "scanner ended before a complete frame")
	return f
}

type sseReadResult struct {
	frame sseFrame
	err   error
}

func readSSEFrameWithin(
	t *testing.T,
	scanner *bufio.Scanner,
	timeout time.Duration,
	stop func(),
) sseFrame {
	t.Helper()
	result := make(chan sseReadResult, 1)
	go func() {
		var f sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				f.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.Data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if f.Event != "" {
					result <- sseReadResult{frame: f}
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			result <- sseReadResult{err: err}
			return
		}
		result <- sseReadResult{err: io.ErrUnexpectedEOF}
	}()

	select {
	case got := <-result:
		require.NoError(t, got.err)
		return got.frame
	case <-time.After(timeout):
		if stop != nil {
			stop()
		}
		require.FailNow(t, "timed out reading SSE frame")
		return sseFrame{}
	}
}

func TestSSE_FrameIncludesID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/events")
	require.NoError(err)
	defer resp.Body.Close()

	require.Eventually(func() bool {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()
		return len(s.hub.subscribers) == 1
	}, 2*time.Second, 10*time.Millisecond)

	gotID := s.hub.Broadcast(Event{Type: "data_changed", Data: map[string]string{"k": "v"}})

	scanner := bufio.NewScanner(resp.Body)
	f := readSSEFrame(t, scanner)
	assert.Equal("data_changed", f.Event)
	assert.JSONEq(`{"k":"v"}`, f.Data)
	assert.Equal("1", f.ID)
	assert.Equal(uint64(1), gotID, "Broadcast should return the assigned id")
}

func TestSSE_LastEventIDHeaderReplaysMissedEvents(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	// Prime the ring with three events the client supposedly saw.
	for i := 1; i <= 3; i++ {
		s.hub.Broadcast(Event{Type: "data_changed", Data: i})
	}
	ts := httptest.NewServer(s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	f2 := readSSEFrame(t, scanner)
	f3 := readSSEFrame(t, scanner)
	assert.Equal("2", f2.ID)
	assert.Equal("3", f3.ID)
	assert.Equal("data_changed", f2.Event)
	assert.Equal("data_changed", f3.Event)
}

func TestSSE_SinceQueryReplaysMissedEvents(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	for i := 1; i <= 3; i++ {
		s.hub.Broadcast(Event{Type: "data_changed", Data: i})
	}
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/events?since=2")
	require.NoError(err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	f := readSSEFrame(t, scanner)
	assert.Equal("3", f.ID)
	assert.Equal("data_changed", f.Event)
}

func TestSSE_LastEventIDHeaderOverridesSinceQuery(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	for i := 1; i <= 5; i++ {
		s.hub.Broadcast(Event{Type: "data_changed", Data: i})
	}
	ts := httptest.NewServer(s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events?since=1", nil,
	)
	require.NoError(err)
	req.Header.Set("Last-Event-ID", "4")
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	f := readSSEFrame(t, scanner)
	// Header wins, only id=5 replays.
	assert.Equal("5", f.ID)
}

func TestSSE_StaleCursorEmitsReconnectStale(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := openTestDB(t)
	s := newServerWithHub(t, database, NewEventHubWithCapacity(4))
	ts := httptest.NewServer(s)
	defer ts.Close()

	// Fill past capacity so cursor 2 falls out of the ring.
	for i := 1; i <= 10; i++ {
		s.hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	req.Header.Set("Last-Event-ID", "2")
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	f := readSSEFrame(t, scanner)
	assert.Equal("reconnect.stale", f.Event)
	assert.Equal("11", f.ID, "synthetic id should follow last broadcast id")

	// And a new broadcast continues from id 12.
	require.Eventually(func() bool {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()
		return len(s.hub.subscribers) == 1
	}, 2*time.Second, 10*time.Millisecond)
	s.hub.Broadcast(Event{Type: "data_changed", Data: "post-stale"})
	live := readSSEFrame(t, scanner)
	assert.Equal("12", live.ID)
}

func TestSSE_InvalidCursorTreatedAsNoCursor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	// Cache a sync_status the no-cursor path delivers on subscribe.
	s.hub.Broadcast(Event{Type: "sync_status", Data: map[string]bool{"running": false}})

	ts := httptest.NewServer(s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	req.Header.Set("Last-Event-ID", "not-a-number")
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	f := readSSEFrame(t, scanner)
	assert.Equal("sync_status", f.Event,
		"unparsable cursor falls back to no-cursor + cached delivery")
}

func TestSSE_CursorAtHeadReplaysNothing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	for i := 1; i <= 3; i++ {
		s.hub.Broadcast(Event{Type: "data_changed", Data: i})
	}
	ts := httptest.NewServer(s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	req.Header.Set("Last-Event-ID", "3")
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	// Wait for subscribe so the broadcast below isn't dropped.
	require.Eventually(func() bool {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()
		return len(s.hub.subscribers) == 1
	}, 2*time.Second, 10*time.Millisecond)

	id := s.hub.Broadcast(Event{Type: "data_changed", Data: "future"})

	scanner := bufio.NewScanner(resp.Body)
	f := readSSEFrame(t, scanner)
	assert.Equal("4", f.ID)
	assert.Equal(uint64(4), id)
}

type blockingFlushController struct {
	mu               sync.Mutex
	flushes          int
	firstFlushCalled chan struct{}
	releaseFirst     chan struct{}
}

func newBlockingFlushController() *blockingFlushController {
	return &blockingFlushController{
		firstFlushCalled: make(chan struct{}),
		releaseFirst:     make(chan struct{}),
	}
}

func (c *blockingFlushController) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *blockingFlushController) Flush() error {
	c.mu.Lock()
	c.flushes++
	flushes := c.flushes
	if flushes == 1 {
		close(c.firstFlushCalled)
	}
	c.mu.Unlock()

	if flushes == 1 {
		<-c.releaseFirst
	}
	return nil
}

func TestSSE_ReplaySkipsLiveEventQueuedBeforeSnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	for i := 1; i <= 2; i++ {
		s.hub.Broadcast(Event{Type: "data_changed", Data: i})
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()

	rc := newBlockingFlushController()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		s.serveSSE(ctx, writer, rc, 2, true)
	}()

	select {
	case <-rc.firstFlushCalled:
	case <-time.After(2 * time.Second):
		require.FailNow("serveSSE did not reach the initial flush")
	}

	// This event is queued on the live subscriber and recorded in the
	// replay ring before RingSnapshotSince runs.
	id3 := s.hub.Broadcast(Event{Type: "data_changed", Data: 3})
	assert.Equal(uint64(3), id3)
	close(rc.releaseFirst)

	scanner := bufio.NewScanner(reader)
	f3 := readSSEFrameWithin(t, scanner, 2*time.Second, func() {
		cancel()
		writer.Close()
	})
	assert.Equal("3", f3.ID)

	id4 := s.hub.Broadcast(Event{Type: "data_changed", Data: 4})
	assert.Equal(uint64(4), id4)
	f4 := readSSEFrameWithin(t, scanner, 2*time.Second, func() {
		cancel()
		writer.Close()
	})
	assert.Equal("4", f4.ID)

	cancel()
	writer.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow("serveSSE did not exit after cancellation")
	}
}

func TestSSE_FutureCursorEmitsReconnectStaleThenLiveEvents(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	s := newTestServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	req.Header.Set("Last-Event-ID", "99")
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	stale := readSSEFrameWithin(t, scanner, 2*time.Second, func() {
		resp.Body.Close()
	})
	assert.Equal("reconnect.stale", stale.Event)
	assert.Equal("1", stale.ID)

	require.Eventually(func() bool {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()
		return len(s.hub.subscribers) == 1
	}, 2*time.Second, 10*time.Millisecond)
	id := s.hub.Broadcast(Event{Type: "data_changed", Data: "after-stale"})
	assert.Equal(uint64(2), id)
	live := readSSEFrameWithin(t, scanner, 2*time.Second, func() {
		resp.Body.Close()
	})
	assert.Equal("2", live.ID)
}

func TestParseLastEventID_HeaderWins(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events?since=42", nil)
	r.Header.Set("Last-Event-ID", "99")
	got, ok := parseLastEventID(r)
	assert.True(t, ok)
	assert.Equal(t, uint64(99), got)
}

func TestParseLastEventID_FallsBackToQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events?since=42", nil)
	got, ok := parseLastEventID(r)
	assert.True(t, ok)
	assert.Equal(t, uint64(42), got)
}

func TestParseLastEventID_AbsentMeansNoCursor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	_, ok := parseLastEventID(r)
	assert.False(t, ok)
}

func TestParseLastEventID_InvalidHeaderFallsBackToQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events?since=7", nil)
	r.Header.Set("Last-Event-ID", "garbage")
	got, ok := parseLastEventID(r)
	assert.True(t, ok)
	assert.Equal(t, uint64(7), got)
}

func TestParseLastEventID_AllUnparsableMeansNoCursor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events?since=abc", nil)
	r.Header.Set("Last-Event-ID", "xyz")
	_, ok := parseLastEventID(r)
	assert.False(t, ok)
}

// newServerWithHub builds a Server bypassing newServer's
// NewEventHub allocation, useful for tests that need a non-default
// ring capacity.
func newServerWithHub(t *testing.T, database *db.DB, hub *EventHub) *Server {
	t.Helper()
	s := New(database, nil, nil, "/", nil, ServerOptions{})
	// Replace the hub atomically before any subscribers exist.
	s.hub.Close()
	s.hub = hub
	return s
}
