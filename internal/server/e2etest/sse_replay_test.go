package e2etest

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/server"
)

// sseFrame is one parsed SSE record. The hub's daemon-level tests parse
// the same shape; this duplicate exists so the e2etest package does not
// import the server package's test-only symbols.
type sseFrame struct {
	ID    string
	Event string
	Data  string
}

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

// waitForSubscribe spins until the hub has at least one subscriber
// registered or the deadline fires. Broadcasting before this returns
// would race with the subscribe call inside serveSSE.
func waitForSubscribe(t *testing.T, srv *server.Server, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return srv.SubscriberCount() == want
	}, 2*time.Second, 5*time.Millisecond,
		"expected %d subscribers", want)
}

func TestE2E_SSEReconnectReplaysMissedEvents(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	defer gracefulShutdown(t, srv)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Connection #1: confirm we receive ids 1..3 live.
	req1, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	resp1, err := ts.Client().Do(req1)
	require.NoError(err)
	waitForSubscribe(t, srv, 1)

	for i := 1; i <= 3; i++ {
		srv.Hub().Broadcast(server.Event{Type: "data_changed", Data: i})
	}

	scanner1 := bufio.NewScanner(resp1.Body)
	var seen []string
	for range 3 {
		f := readSSEFrame(t, scanner1)
		seen = append(seen, f.ID)
	}
	assert.Equal([]string{"1", "2", "3"}, seen)

	// Drop the first connection — the slow-consumer eviction path or
	// context-cancel cleanup brings the subscriber count back to 0.
	resp1.Body.Close()
	require.Eventually(func() bool {
		return srv.SubscriberCount() == 0
	}, 2*time.Second, 5*time.Millisecond)

	// Broadcast two more while disconnected.
	for i := 4; i <= 5; i++ {
		srv.Hub().Broadcast(server.Event{Type: "data_changed", Data: i})
	}

	// Connection #2: reconnect with Last-Event-ID: 3.
	req2, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	req2.Header.Set("Last-Event-ID", "3")
	resp2, err := ts.Client().Do(req2)
	require.NoError(err)
	defer resp2.Body.Close()

	scanner2 := bufio.NewScanner(resp2.Body)
	// First two frames are the replay of ids 4 and 5.
	f4 := readSSEFrame(t, scanner2)
	f5 := readSSEFrame(t, scanner2)
	assert.Equal("4", f4.ID)
	assert.Equal("5", f5.ID)

	// Live event 6 also arrives.
	waitForSubscribe(t, srv, 1)
	srv.Hub().Broadcast(server.Event{Type: "data_changed", Data: 6})
	f6 := readSSEFrame(t, scanner2)
	assert.Equal("6", f6.ID)
}

func TestE2E_SSESinceQueryWorks(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	defer gracefulShutdown(t, srv)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Pre-broadcast so the ring has events 1..3.
	for i := 1; i <= 3; i++ {
		srv.Hub().Broadcast(server.Event{Type: "data_changed", Data: i})
	}

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events?since=2", nil,
	)
	require.NoError(err)
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	f := readSSEFrame(t, scanner)
	assert.Equal("3", f.ID)
	assert.Equal("data_changed", f.Event)
}

func TestE2E_SSEStaleCursorEmitsReconnectStale(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _, _ := setupTestServerWithSSEBufferSize(t, 4)
	defer gracefulShutdown(t, srv)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Roll the ring well past capacity.
	for i := 1; i <= 10; i++ {
		srv.Hub().Broadcast(server.Event{Type: "data_changed", Data: i})
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
	assert.Equal("11", f.ID, "synthetic id follows the last real broadcast")

	// Live frame after the stale signal flows normally.
	waitForSubscribe(t, srv, 1)
	srv.Hub().Broadcast(server.Event{Type: "data_changed", Data: "post-stale"})
	live := readSSEFrame(t, scanner)
	assert.Equal("12", live.ID)
}

func TestE2E_SSEFutureCursorEmitsReconnectStale(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	defer gracefulShutdown(t, srv)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	req.Header.Set("Last-Event-ID", "99")
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	stale := readSSEFrame(t, scanner)
	assert.Equal("reconnect.stale", stale.Event)
	assert.Equal("1", stale.ID)

	waitForSubscribe(t, srv, 1)
	srv.Hub().Broadcast(server.Event{Type: "data_changed", Data: "after-stale"})
	live := readSSEFrame(t, scanner)
	assert.Equal("2", live.ID)
}

func TestE2E_SSEFirstConnectGetsCachedSyncStatus(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	defer gracefulShutdown(t, srv)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	srv.Hub().Broadcast(server.Event{
		Type: "sync_status",
		Data: map[string]any{"running": false},
	})

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	f := readSSEFrame(t, scanner)
	assert.Equal("sync_status", f.Event)
	assert.Equal("1", f.ID, "cached event keeps its original assigned id")
}

func TestE2E_SSEFramesAlwaysIncludeID(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	defer gracefulShutdown(t, srv)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil,
	)
	require.NoError(err)
	resp, err := ts.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	waitForSubscribe(t, srv, 1)
	srv.Hub().Broadcast(server.Event{Type: "data_changed", Data: 1})

	// Read raw bytes until we see a complete frame; the id: line must
	// precede the event: line in the on-the-wire ordering the SSE spec
	// allows.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	body := readUntilBlankLine(ctx, t, resp.Body)
	assert.Contains(body, "id: 1\n", "frame must include the id field")
	assert.Contains(body, "event: data_changed\n")
}

// readUntilBlankLine reads bytes until it sees \n\n indicating the end
// of one frame, with a timeout to keep flaky tests from hanging.
func readUntilBlankLine(
	ctx context.Context, t *testing.T, r io.Reader,
) string {
	t.Helper()
	done := make(chan struct{})
	var buf strings.Builder
	go func() {
		defer close(done)
		one := make([]byte, 1)
		for {
			n, err := r.Read(one)
			if n > 0 {
				buf.WriteByte(one[0])
				if strings.HasSuffix(buf.String(), "\n\n") {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
		return buf.String()
	case <-ctx.Done():
		require.FailNow(t, "timed out reading SSE frame")
		return ""
	}
}
