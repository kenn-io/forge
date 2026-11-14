package workspaceapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var (
	acpSpanRecorderOnce sync.Once
	acpSpanRecorder     *tracetest.SpanRecorder
)

// recordACPSpans installs one recorder for the whole test binary: a tracer
// taken from the global provider stays bound to the first provider set.
func recordACPSpans() *tracetest.SpanRecorder {
	acpSpanRecorderOnce.Do(func() {
		acpSpanRecorder = tracetest.NewSpanRecorder()
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(acpSpanRecorder)))
	})
	return acpSpanRecorder
}

// firstStateSpan returns the ended first-state span of the chat named key, or
// nil while none has ended.
func firstStateSpan(recorder *tracetest.SpanRecorder, key string) sdktrace.ReadOnlySpan {
	for _, span := range recorder.Ended() {
		if span.Name() != "workspace.acp.first_state" {
			continue
		}
		for _, kv := range span.Attributes() {
			if kv.Key == "session.key" && kv.Value.AsString() == key {
				return span
			}
		}
	}
	return nil
}

func spanAttribute(span sdktrace.ReadOnlySpan, key attribute.Key) string {
	for _, kv := range span.Attributes() {
		if kv.Key == key {
			return kv.Value.String()
		}
	}
	return ""
}

// snapshotChat is a chat whose socket is told to send its state at once.
type snapshotChat struct {
	stallingChat
	snapshot    []byte
	snapshotErr error
}

func (c *snapshotChat) Snapshot() ([]byte, error) { return c.snapshot, c.snapshotErr }
func (c *snapshotChat) Subscribe() (<-chan struct{}, func()) {
	changes := make(chan struct{}, 1)
	changes <- struct{}{}
	return changes, func() {}
}

var tracedChatKeys atomic.Int64

// serveTracedChat serves chat under a session key no other run shares, since
// the recorder outlives each test.
func serveTracedChat(t *testing.T, chat workspaceChat) (string, *websocket.Conn, func()) {
	t.Helper()
	key := fmt.Sprintf("%s-%d", t.Name(), tracedChatKeys.Add(1))
	mux := http.NewServeMux()
	mux.HandleFunc("/workspaces/{id}/sessions/{session_key}", func(w http.ResponseWriter, r *http.Request) { serveACP(w, r, chat) })
	server := httptest.NewServer(mux)
	conn, resp, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/workspaces/ws-trace/sessions/"+key, nil)
	if err != nil && resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	require.NoError(t, err)
	return key, conn, func() { _ = conn.CloseNow(); server.Close() }
}

// The first-state span ends when the chat's state goes out, while the stream
// it starts stays open, so it measures how long a pane waits to load.
func TestACPFirstStateSpanEndsWhenTheStateIsSent(t *testing.T) {
	recorder := recordACPSpans()
	chat := &snapshotChat{snapshot: []byte(`{"messageCursor":3}`)}
	key, conn, closeChat := serveTracedChat(t, chat)
	defer closeChat()

	_, data, err := conn.Read(t.Context())
	require.NoError(t, err)
	assert.JSONEq(t, `{"messageCursor":3}`, string(data))
	var span sdktrace.ReadOnlySpan
	require.Eventually(t, func() bool { span = firstStateSpan(recorder, key); return span != nil }, 2*time.Second, 5*time.Millisecond)

	assert.Equal(t, "sent", spanAttribute(span, "outcome"))
	assert.Equal(t, "ws-trace", spanAttribute(span, "workspace.id"))
	assert.Equal(t, "false", spanAttribute(span, "acp.dormant"))
	assert.Equal(t, codes.Unset, span.Status().Code)
	// The socket is still served after the span ended.
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"heartbeat"}`)))
	_, data, err = conn.Read(t.Context())
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"heartbeat"}`, string(data))
}

// A chat whose owner fails to answer for its state records why it never loaded.
func TestACPFirstStateSpanFailsWhenTheChatCannotSnapshot(t *testing.T) {
	recorder := recordACPSpans()
	chat := &snapshotChat{snapshotErr: errors.New("owner connection lost")}
	key, conn, closeChat := serveTracedChat(t, chat)
	defer closeChat()

	_, _, err := conn.Read(t.Context())
	require.Error(t, err)
	var span sdktrace.ReadOnlySpan
	require.Eventually(t, func() bool { span = firstStateSpan(recorder, key); return span != nil }, 2*time.Second, 5*time.Millisecond)

	assert.Equal(t, "snapshot_failed", spanAttribute(span, "outcome"))
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Equal(t, "owner connection lost", span.Status().Description)
}

// blockingSnapshotChat is a chat whose owner never answers for its state.
type blockingSnapshotChat struct {
	snapshotChat
	release chan struct{}
}

func (c *blockingSnapshotChat) Snapshot() ([]byte, error) {
	<-c.release
	return []byte(`{}`), nil
}

// An owner that never answers holds the handler in Snapshot. The span must
// still end when the browser gives up, or the hang never reaches a trace.
func TestACPFirstStateSpanEndsWhenTheSocketClosesDuringABlockedSnapshot(t *testing.T) {
	recorder := recordACPSpans()
	chat := &blockingSnapshotChat{release: make(chan struct{})}
	key, conn, closeChat := serveTracedChat(t, chat)
	defer closeChat()
	defer close(chat.release)

	require.NoError(t, conn.Close(websocket.StatusNormalClosure, "gave up waiting"))
	var span sdktrace.ReadOnlySpan
	require.Eventually(t, func() bool { span = firstStateSpan(recorder, key); return span != nil }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, "closed", spanAttribute(span, "outcome"))
}
