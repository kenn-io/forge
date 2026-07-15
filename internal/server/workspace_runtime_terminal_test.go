package server

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

	"go.kenn.io/middleman/internal/workspace/localruntime"
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

func runtimeTerminalTestServer(
	t *testing.T,
	attachment *localruntime.Attachment,
) (string, <-chan struct{}) {
	t.Helper()
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			defer close(handlerDone)
			new(Server).serveRuntimeTerminal(
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
