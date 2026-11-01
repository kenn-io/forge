package fleetapi

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"go.kenn.io/forge/internal/fleettunnel"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestMemberRequestsJoinTheSpokeToTheHubTrace(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	spokeContexts := make(chan trace.SpanContext, 1)
	spoke := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spokeContexts <- trace.SpanContextFromContext(propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header)))
		w.WriteHeader(http.StatusNoContent)
	})
	tunnels := fleettunnel.NewRegistry()
	hubEnd, spokeEnd := net.Pipe()
	go func() { _ = fleettunnel.Serve(t.Context(), spokeEnd, spoke) }()
	go func() { _ = tunnels.Serve(t.Context(), testMemberNodeID, hubEnd) }()
	require.Eventually(func() bool { return tunnels.Connected(testMemberNodeID) },
		5*time.Second, 5*time.Millisecond)
	srv := New(Deps{DB: dbtest.Open(t), Tunnels: tunnels})
	recorder := tracetest.NewSpanRecorder()
	ctx, caller := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)).Tracer("test").Start(t.Context(), "fleet snapshot")

	clients := srv.memberClientsFor(testMemberNodeID, "https://spoke.invalid", false)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://spoke.invalid/api/v1/snapshot/raw", nil)
	require.NoError(err)
	response, err := clients.rest.Do(request)
	require.NoError(err)
	require.NoError(response.Body.Close())
	caller.End()

	assert.Equal(http.StatusNoContent, response.StatusCode)
	var client sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.SpanKind() == trace.SpanKindClient {
			require.Nil(client, "one span per member request")
			client = span
		}
	}
	require.NotNil(client)
	assert.Equal("GET spoke.invalid", client.Name())
	assert.Equal(caller.SpanContext().SpanID(), client.Parent().SpanID())
	spokeContext := <-spokeContexts
	assert.Equal(caller.SpanContext().TraceID(), spokeContext.TraceID())
	assert.Equal(client.SpanContext().SpanID(), spokeContext.SpanID())
}
