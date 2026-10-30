package workspaceapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"go.kenn.io/forge/internal/ptysize"
	"go.kenn.io/forge/internal/terminalwebsocket"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestServeRuntimeTerminalNegotiatesCompression(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output: make(chan []byte),
			Done:   make(chan struct{}),
		},
	)
	wsURL, handlerDone := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if resp != nil {
		t.Cleanup(func() {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		})
	}
	require.NoError(err)
	require.NotNil(resp)
	require.Contains(
		resp.Header.Get("Sec-WebSocket-Extensions"),
		"permessage-deflate",
	)

	require.NoError(conn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case <-handlerDone:
	case <-ctx.Done():
		require.Fail("terminal handler did not return")
	}
}

func TestServeRuntimeTerminalAnswersHeartbeat(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output: make(chan []byte),
			Done:   make(chan struct{}),
		},
	)
	wsURL, handlerDone := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, wsHTTPResp, err := websocket.Dial(ctx, wsURL, nil)
	if wsHTTPResp != nil {
		t.Cleanup(func() {
			if wsHTTPResp != nil && wsHTTPResp.Body != nil {
				_ = wsHTTPResp.Body.Close()
			}
		})
	}
	require.NoError(err)
	require.NoError(conn.Write(
		ctx, websocket.MessageText,
		[]byte(terminalwebsocket.HeartbeatMessage),
	))
	typ, data, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(websocket.MessageText, typ)
	require.JSONEq(terminalwebsocket.HeartbeatMessage, string(data))

	require.NoError(conn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case <-handlerDone:
	case <-ctx.Done():
		require.Fail("terminal handler did not return")
	}
}

func TestServeRuntimeTerminalForwardsBufferedReplayBeforeRefresh(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	replay := []byte("buffered replay")
	output := make(chan []byte, 1)
	output <- replay
	done := make(chan struct{})
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseRefresh:
		default:
			close(releaseRefresh)
		}
	})
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output: output,
			Done:   done,
			Resize: func(ptysize.Geometry) error { return nil },
			Refresh: func(ctx context.Context, statusOnly bool) error {
				require.False(statusOnly)
				close(refreshStarted)
				select {
				case <-releaseRefresh:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		},
	)
	wsURL, handlerDone := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, wsHTTPResp, err := websocket.Dial(ctx, wsURL+"?cols=80&rows=24", nil)
	if wsHTTPResp != nil {
		t.Cleanup(func() {
			if wsHTTPResp != nil && wsHTTPResp.Body != nil {
				_ = wsHTTPResp.Body.Close()
			}
		})
	}
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	select {
	case <-refreshStarted:
	case <-ctx.Done():
		require.Fail("initial refresh did not start")
	}

	typ, data, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(websocket.MessageBinary, typ)
	require.Equal(replay, data)

	close(releaseRefresh)
	require.NoError(conn.Close(websocket.StatusNormalClosure, "done"))
	select {
	case <-handlerDone:
	case <-ctx.Done():
		require.Fail("terminal handler did not return")
	}
}

func TestForwardAvailableRuntimeOutputReturnsWriteError(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	wantErr := errors.New("write failed")
	replay := []byte("buffered replay")
	output := make(chan []byte, 1)
	output <- replay

	err := forwardAvailableRuntimeOutput(
		t.Context(), time.Second, output,
		func(_ context.Context, data []byte) error {
			require.Equal(replay, data)
			return wantErr
		},
	)

	require.ErrorIs(err, wantErr)
}

func TestForwardAvailableRuntimeOutputBoundsBlockedWrite(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	output := make(chan []byte, 1)
	output <- []byte("buffered replay")

	err := forwardAvailableRuntimeOutput(
		t.Context(), 10*time.Millisecond, output,
		func(ctx context.Context, _ []byte) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)

	require.ErrorIs(err, context.DeadlineExceeded)
}

func TestServeRuntimeTerminalTranslatesReplayBoundary(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	output := make(chan []byte, 1)
	output <- nil
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output: output,
			Done:   make(chan struct{}),
		},
	)
	wsURL, _ := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, wsHTTPResp, err := websocket.Dial(ctx, wsURL, nil)
	if wsHTTPResp != nil {
		t.Cleanup(func() {
			if wsHTTPResp != nil && wsHTTPResp.Body != nil {
				_ = wsHTTPResp.Body.Close()
			}
		})
	}
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	typ, data, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(websocket.MessageText, typ)
	var msg struct {
		Type string `json:"type"`
	}
	require.NoError(json.Unmarshal(data, &msg))
	require.Equal("replay_ready", msg.Type)
}

func TestServeRuntimeTerminalReplayBoundaryDefersInitialResize(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	output := make(chan []byte, 1)
	output <- nil
	resizeCalled := make(chan struct{}, 1)
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output: output,
			Done:   make(chan struct{}),
			Resize: func(ptysize.Geometry) error {
				resizeCalled <- struct{}{}
				return nil
			},
		},
	)
	wsURL, _ := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, wsHTTPResp, err := websocket.Dial(
		ctx,
		wsURL+"?cols=177&rows=41&replay_boundary=1",
		nil,
	)
	if wsHTTPResp != nil {
		t.Cleanup(func() {
			if wsHTTPResp != nil && wsHTTPResp.Body != nil {
				_ = wsHTTPResp.Body.Close()
			}
		})
	}
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	typ, _, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(websocket.MessageText, typ)
	select {
	case <-resizeCalled:
		require.Fail("initial resize ran before the replay boundary")
	default:
	}
}

func TestHandleRuntimeTerminalControlAcknowledgesResizeClaimBeforeReturning(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	events := make([]string, 0, 4)
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			ClaimResize: func(geometry ptysize.Geometry) (bool, error) {
				require.Equal(ptysize.Geometry{
					Cols:        132,
					Rows:        43,
					PixelWidth:  1056,
					PixelHeight: 688,
				}, geometry)
				events = append(events, "claim")
				return true, nil
			},
			Refresh: func(_ context.Context, statusOnly bool) error {
				require.True(statusOnly)
				events = append(events, "refresh")
				return nil
			},
			ResizeSettled: func() {
				events = append(events, "settled")
			},
		},
	)

	heartbeat, err := handleRuntimeTerminalControl(
		t.Context(),
		attachment,
		[]byte(`{"type":"claim_resize","cols":132,"rows":43,"pixel_width":1056,"pixel_height":688}`),
	)
	require.NoError(err)
	require.False(heartbeat)
	events = append(events, "next input")

	require.Equal([]string{"claim", "refresh", "settled", "next input"}, events)
}

func TestRuntimeTerminalStopsBeforeInputWhenResizeClaimSettlementFails(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	input := make(chan []byte, 1)
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output: make(chan []byte),
			Done:   make(chan struct{}),
			Write: func(data []byte) error {
				input <- data
				return nil
			},
			ClaimResize: func(ptysize.Geometry) (bool, error) {
				return true, nil
			},
			Refresh: func(context.Context, bool) error {
				return errors.New("tmux refresh failed")
			},
		},
	)
	wsURL, handlerDone := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, wsHTTPResp, err := websocket.Dial(ctx, wsURL, nil)
	if wsHTTPResp != nil {
		t.Cleanup(func() {
			if wsHTTPResp != nil && wsHTTPResp.Body != nil {
				_ = wsHTTPResp.Body.Close()
			}
		})
	}
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")
	require.NoError(conn.Write(
		ctx, websocket.MessageText,
		[]byte(`{"type":"claim_resize","cols":132,"rows":43}`),
	))
	require.NoError(conn.Write(ctx, websocket.MessageBinary, []byte("input")))
	_, _, err = conn.Read(ctx)
	require.Equal(websocket.StatusNormalClosure, websocket.CloseStatus(err))

	select {
	case <-handlerDone:
	case <-ctx.Done():
		require.Fail("terminal handler did not stop after settlement failure")
	}
	select {
	case data := <-input:
		require.Failf("input reached PTY", "unexpected input %q", data)
	default:
	}
}

func TestServeRuntimeTerminalRestartDetachDoesNotReportSessionExit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output func() <-chan []byte
		done   func() <-chan struct{}
	}{
		{
			name: "pty eof arrives first",
			output: func() <-chan []byte {
				output := make(chan []byte)
				close(output)
				return output
			},
			done: func() <-chan struct{} { return make(chan struct{}) },
		},
		{
			name:   "process done arrives first",
			output: func() <-chan []byte { return make(chan []byte) },
			done: func() <-chan struct{} {
				done := make(chan struct{})
				close(done)
				return done
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			attachment := localruntime.NewAttachmentForTesting(
				localruntime.AttachmentForTestingOptions{
					Output:              tt.output(),
					Done:                tt.done(),
					SessionOutputClosed: func() bool { return true },
					RecoverableDetach:   func() bool { return true },
				},
			)
			wsURL, handlerDone := runtimeTerminalTestServer(t, attachment)

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			conn, wsHTTPResp, err := websocket.Dial(ctx, wsURL, nil)
			if wsHTTPResp != nil && wsHTTPResp.Body != nil {
				t.Cleanup(func() {
					if wsHTTPResp != nil && wsHTTPResp.Body != nil {
						_ = wsHTTPResp.Body.Close()
					}
				})
			}
			require.NoError(err)
			defer conn.Close(websocket.StatusNormalClosure, "done")

			_, _, err = conn.Read(ctx)
			require.Error(err)
			select {
			case <-handlerDone:
			case <-ctx.Done():
				require.Fail("terminal handler did not return after restart detach")
			}
		})
	}
}

func TestServeRuntimeTerminalWaitsForOutputAfterProcessExit(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	output := make(chan []byte, 1)
	done := make(chan struct{})
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output: output,
			Done:   done,
		},
	)
	wsURL, handlerDone := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, wsHTTPResp, err := websocket.Dial(ctx, wsURL, nil)
	if wsHTTPResp != nil {
		t.Cleanup(func() {
			if wsHTTPResp != nil && wsHTTPResp.Body != nil {
				_ = wsHTTPResp.Body.Close()
			}
		})
	}
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	exitCtx, exitCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer exitCancel()
	select {
	case done <- struct{}{}:
	case <-exitCtx.Done():
		require.FailNow("terminal bridge did not consume the exit signal")
	}
	type frame struct {
		typ  websocket.MessageType
		data []byte
		err  error
	}
	firstFrame := make(chan frame, 1)
	go func() {
		typ, data, err := conn.Read(exitCtx)
		firstFrame <- frame{typ: typ, data: data, err: err}
	}()
	select {
	case <-firstFrame:
		require.FailNow("terminal bridge returned before final output arrived")
	case <-time.After(100 * time.Millisecond): //nolint:kennlint // shows no websocket frame arrives during the post-exit window before final output is released
	}
	output <- []byte("final terminal output")
	close(output)

	first := <-firstFrame
	require.NoError(first.err)
	require.Equal(websocket.MessageBinary, first.typ)
	require.Equal("final terminal output", string(first.data))

	typ, data, err := conn.Read(exitCtx)
	require.NoError(err)
	require.Equal(websocket.MessageText, typ)
	var msg struct {
		Type string `json:"type"`
	}
	require.NoError(json.Unmarshal(data, &msg))
	require.Equal("exited", msg.Type)

	select {
	case <-handlerDone:
	case <-exitCtx.Done():
		require.Fail("terminal handler did not return after session exit")
	}
}

func runtimeTerminalTestServer(
	t *testing.T,
	attachment *localruntime.Attachment,
) (string, <-chan struct{}) {
	t.Helper()
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			defer close(handlerDone)
			new(Handler).serveRuntimeTerminal(
				w,
				r,
				attachment,
				trace.SpanFromContext(r.Context()),
				func() {},
			)
		},
	))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), handlerDone
}

func TestParseRuntimeTerminalGeometry(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/?cols=132&rows=43&pixel_width=1056&pixel_height=688",
		nil,
	)

	geometry, ok := parseRuntimeTerminalGeometry(req)

	require.True(t, ok)
	assert.Equal(t, ptysize.Geometry{
		Cols:        132,
		Rows:        43,
		PixelWidth:  1056,
		PixelHeight: 688,
	}, geometry)
}
