package e2etest

import (
	"bufio"
	"context"
	"encoding/json"
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

// TestSSEContractPinDeliversCachedSyncStatusFrame is a paving wire-level test:
// it exercises the /api/v1/events endpoint through a real httptest.NewServer
// socket, which is the only transport that faithfully simulates flushed
// streaming I/O. The in-process recorder buffers writes until the handler
// returns, so a recorder-based test would not catch middleware that strips
// or rewrites streaming response headers, nor would it observe the SSE frame
// boundary.
//
// The test pins the visible contract for a single cached sync_status event:
// status 200, the streaming Content-Type, Cache-Control: no-cache, and the
// first SSE frame's event/data lines. Pre-broadcasting through the hub
// before the server starts removes the broadcast / read race a non-cached
// first event would introduce.
func TestSSEContractPinDeliversCachedSyncStatusFrame(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	srv.Hub().Broadcast(server.Event{
		Type: "sync_status",
		Data: map[string]bool{"running": false},
	})

	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
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

	eventType, dataLine := readFirstSSEFrame(t, resp.Body)
	require.Equal("sync_status", eventType)

	var payload map[string]bool
	require.NoError(json.Unmarshal([]byte(dataLine), &payload))
	assert.Equal(map[string]bool{"running": false}, payload)
}

// readFirstSSEFrame reads from r until it sees a blank-line terminator,
// returning the event type and the bytes after "data: " on the data line.
// The SSE wire format is one or more "field: value" lines terminated by a
// blank line.
func readFirstSSEFrame(t *testing.T, r io.Reader) (string, string) {
	t.Helper()
	scanner := bufio.NewScanner(r)
	var eventType, data string
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = rest
		}
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			data = rest
		}
		if line == "" && eventType != "" {
			return eventType, data
		}
	}
	require.NoError(t, scanner.Err())
	require.FailNow(t, "did not read a complete SSE frame")
	return "", ""
}
