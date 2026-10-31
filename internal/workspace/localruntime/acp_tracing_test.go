package localruntime

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/ptyowner"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

var (
	spanRecorderOnce sync.Once
	spanRecorder     *tracetest.SpanRecorder
)

// recordSpans installs one recorder for the whole test binary: a tracer taken
// from the global provider stays bound to the first provider set.
func recordSpans() *tracetest.SpanRecorder {
	spanRecorderOnce.Do(func() {
		spanRecorder = tracetest.NewSpanRecorder()
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder)))
		otel.SetTextMapPropagator(propagation.TraceContext{})
	})
	return spanRecorder
}

func endedSpan(t *testing.T, recorder *tracetest.SpanRecorder, traceID trace.TraceID, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.SpanContext().TraceID() == traceID && span.Name() == name {
			return span
		}
	}
	require.Failf(t, "span not recorded", "no ended %q span in trace %s", name, traceID)
	return nil
}

func TestACPHandshakeStepSpansFailWhenTheirReplyIsRejected(t *testing.T) {
	recorder := recordSpans()
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	command := []string{executable, "-test.run=^TestACPStdioHelper$"}

	for step, fixtureSwitch := range map[string]string{
		"acp.agent.initialize":  "KENN_FORGE_ACP_BAD_VERSION",
		"acp.agent.session_new": "KENN_FORGE_ACP_EMPTY_SESSION",
	} {
		t.Run(step, func(t *testing.T) {
			t.Setenv(fixtureSwitch, "1")
			ctx, parent := otel.Tracer("test").Start(t.Context(), "test")

			_, err := startACPSession(ctx, command, t.TempDir(), nil, nil, nil, nil, nil)
			parent.End()

			require.Error(t, err)
			assert.Equal(t, codes.Error, endedSpan(t, recorder, parent.SpanContext().TraceID(), step).Status().Code)
		})
	}
}

func TestACPHostInitializeSpanJoinsTheTraceThatStartedTheHost(t *testing.T) {
	assert := assert.New(t)
	recorder := recordSpans()
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	root := t.TempDir()
	paths, err := ptyowner.NewSessionPaths(root, "host-trace")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(paths.Dir, 0o700))
	if paths.SocketDir != "" {
		require.NoError(t, os.MkdirAll(paths.SocketDir, 0o700))
	}
	ctx, parent := otel.Tracer("test").Start(t.Context(), "test")
	data, err := json.Marshal(acpHostConfig{
		Command: []string{executable, "-test.run=^TestACPStdioHelper$"}, Dir: paths.Dir, Socket: paths.Socket,
		Pointer: filepath.Join(root, "pointer.json"), Generation: "trace", TraceContext: injectTraceContext(ctx),
	})
	require.NoError(t, err)
	configPath := filepath.Join(paths.Dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, data, 0o600))

	hostCtx, stopHost := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- RunACPHost(hostCtx, configPath) }()
	conn, err := dialStartingACPProcess(t.Context(), paths.Dir, paths.Socket, nil)
	require.NoError(t, err)
	_ = conn.Close()
	stopHost()
	<-done
	parent.End()

	traceID := parent.SpanContext().TraceID()
	run := endedSpan(t, recorder, traceID, "acp.host.run")
	initialize := endedSpan(t, recorder, traceID, "acp.host.initialize")
	assert.Equal(parent.SpanContext().SpanID(), run.Parent().SpanID())
	assert.Equal(run.SpanContext().SpanID(), initialize.Parent().SpanID())
	assert.Equal(codes.Unset, initialize.Status().Code)
	assert.False(initialize.EndTime().After(run.EndTime()), "initialize ends before the host listens")
}
