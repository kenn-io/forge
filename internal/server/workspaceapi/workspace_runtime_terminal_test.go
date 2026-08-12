package workspaceapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestServeRuntimeTerminalForwardsBufferedReplayBeforeRefresh(t *testing.T) {
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
			Resize: func(_, _ int) error { return nil },
			Refresh: func(ctx context.Context) error {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL+"?cols=80&rows=24", nil)
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
	require := require.New(t)
	wantErr := errors.New("write failed")
	replay := []byte("buffered replay")
	output := make(chan []byte, 1)
	output <- replay

	err := forwardAvailableRuntimeOutput(
		context.Background(), time.Second, output,
		func(_ context.Context, data []byte) error {
			require.Equal(replay, data)
			return wantErr
		},
	)

	require.ErrorIs(err, wantErr)
}

func TestForwardAvailableRuntimeOutputBoundsBlockedWrite(t *testing.T) {
	require := require.New(t)
	output := make(chan []byte, 1)
	output <- []byte("buffered replay")

	err := forwardAvailableRuntimeOutput(
		context.Background(), 10*time.Millisecond, output,
		func(ctx context.Context, _ []byte) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)

	require.ErrorIs(err, context.DeadlineExceeded)
}

func TestServeRuntimeTerminalTranslatesReplayBoundary(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
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
	require := require.New(t)
	output := make(chan []byte, 1)
	output <- nil
	resizeCalled := make(chan struct{}, 1)
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output: output,
			Done:   make(chan struct{}),
			Resize: func(_, _ int) error {
				resizeCalled <- struct{}{}
				return nil
			},
		},
	)
	wsURL, _ := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(
		ctx,
		wsURL+"?cols=177&rows=41&replay_boundary=1",
		nil,
	)
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
	require := require.New(t)
	events := make([]string, 0, 4)
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			ClaimResize: func(cols, rows int) (bool, error) {
				require.Equal(132, cols)
				require.Equal(43, rows)
				events = append(events, "claim")
				return true, nil
			},
			Refresh: func(context.Context) error {
				events = append(events, "refresh")
				return nil
			},
			ResizeSettled: func() {
				events = append(events, "settled")
			},
		},
	)

	require.NoError(handleRuntimeTerminalControl(
		context.Background(),
		attachment,
		[]byte(`{"type":"claim_resize","cols":132,"rows":43}`),
	))
	events = append(events, "next input")

	require.Equal([]string{"claim", "refresh", "settled", "next input"}, events)
}

func TestRuntimeTerminalStopsBeforeInputWhenResizeClaimSettlementFails(t *testing.T) {
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
			ClaimResize: func(_, _ int) (bool, error) {
				return true, nil
			},
			Refresh: func(context.Context) error {
				return errors.New("tmux refresh failed")
			},
		},
	)
	wsURL, handlerDone := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
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

func TestServeRuntimeTerminalClosedOutputStillReportsSessionExit(t *testing.T) {
	require := require.New(t)
	output := make(chan []byte)
	close(output)
	done := make(chan struct{})
	attachment := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output:              output,
			Done:                done,
			Resize:              func(_, _ int) error { return nil },
			Refresh:             func(context.Context) error { return nil },
			SessionOutputClosed: func() bool { return true },
		},
	)
	wsURL, handlerDone := runtimeTerminalTestServer(t, attachment)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(
		ctx, wsURL+"?cols=80&rows=24", nil,
	)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	typ, data, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(websocket.MessageText, typ)
	var msg struct {
		Type string `json:"type"`
	}
	require.NoError(json.Unmarshal(data, &msg))
	require.Equal("exited", msg.Type)
	_ = conn.Close(websocket.StatusNormalClosure, "done")
	select {
	case <-handlerDone:
	case <-ctx.Done():
		require.Fail("terminal handler did not return after session exit")
	}
}

func TestServeRuntimeTerminalRestartDetachDoesNotReportSessionExit(t *testing.T) {
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
					Output:                   tt.output(),
					Done:                     tt.done(),
					SessionOutputClosed:      func() bool { return true },
					DetachedForServerRestart: func() bool { return true },
				},
			)
			wsURL, handlerDone := runtimeTerminalTestServer(t, attachment)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, wsURL, nil)
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

func TestServeRuntimeTerminalDrainsDelayedFinalOutputBeforeSessionExit(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	close(done)
	go func() {
		time.Sleep(200 * time.Millisecond)
		output <- []byte("final terminal output")
		close(output)
	}()

	typ, data, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(websocket.MessageBinary, typ)
	require.Equal("final terminal output", string(data))

	typ, data, err = conn.Read(ctx)
	require.NoError(err)
	require.Equal(websocket.MessageText, typ)
	var msg struct {
		Type string `json:"type"`
	}
	require.NoError(json.Unmarshal(data, &msg))
	require.Equal("exited", msg.Type)

	select {
	case <-handlerDone:
	case <-ctx.Done():
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

func TestClampTerminalDim(t *testing.T) {
	assert := assert.New(t)
	cases := []struct {
		name string
		in   int
		want uint16
	}{
		{"zero floors to one", 0, 1},
		{"negative floors to one", -5, 1},
		{"minimum", 1, 1},
		{"typical", 120, 120},
		{"uint16 max", math.MaxUint16, math.MaxUint16},
		{"above uint16 max caps", math.MaxUint16 + 1, math.MaxUint16},
		{"large value caps", 1_000_000, math.MaxUint16},
	}
	for _, tc := range cases {
		assert.Equalf(tc.want, clampTerminalDim(tc.in), "case %s", tc.name)
	}
}
