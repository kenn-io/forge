package tracing

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// withGlobalTraceContext installs a W3C propagator for the test, so a
// transport that sends no trace context proves it ignores the global one.
func withGlobalTraceContext(t *testing.T) {
	t.Helper()
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
}

func startCaller(t *testing.T) (context.Context, trace.Span, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	ctx, span := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)).Tracer("test").Start(t.Context(), "caller")
	return ctx, span, recorder
}

func get(ctx context.Context, t *testing.T, transport http.RoundTripper, target string, header http.Header) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	require.NoError(t, err)
	maps.Copy(request.Header, header)
	response, err := (&http.Client{Transport: transport}).Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}

func clientSpans(recorder *tracetest.SpanRecorder) []sdktrace.ReadOnlySpan {
	var spans []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.SpanKind() == trace.SpanKindClient {
			spans = append(spans, span)
		}
	}
	return spans
}

func spanAttribute(span sdktrace.ReadOnlySpan, key string) string {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value.String()
		}
	}
	return ""
}

func TestHTTPTransportRecordsRedactedClientSpanWithoutTraceContext(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	withGlobalTraceContext(t)
	traceparents := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparents <- r.Header.Get("traceparent")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	ctx, caller, recorder := startCaller(t)

	get(ctx, t, HTTPTransport(nil), "http://user:secret-password@"+host+"/repos/7/files?token=secret-token&X-Amz-Signature=secret-signature", nil)
	caller.End()

	assert.Empty(<-traceparents, "a third-party API gets no trace context")
	spans := clientSpans(recorder)
	require.Len(spans, 1)
	span := spans[0]
	assert.Equal("GET "+host, span.Name())
	assert.Equal(caller.SpanContext().TraceID(), span.SpanContext().TraceID())
	assert.Equal(caller.SpanContext().SpanID(), span.Parent().SpanID())
	assert.Equal("http://"+host+"/repos/7/files", spanAttribute(span, "url.full"))
	assert.Equal("200", spanAttribute(span, "http.response.status_code"))
	for _, attr := range span.Attributes() {
		assert.NotContains(attr.Value.String(), "secret", attr.Key)
	}
}

func TestPeerHTTPTransportJoinsThePeerToTheCallerTrace(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	withGlobalTraceContext(t)
	peerContexts := make(chan trace.SpanContext, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerContexts <- trace.SpanContextFromContext(otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header)))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(peer.Close)
	ctx, caller, recorder := startCaller(t)

	get(ctx, t, PeerHTTPTransport(nil), peer.URL+"/api/v1/snapshot/raw", nil)
	caller.End()

	spans := clientSpans(recorder)
	require.Len(spans, 1)
	peerContext := <-peerContexts
	assert.Equal(caller.SpanContext().TraceID(), peerContext.TraceID())
	assert.Equal(spans[0].SpanContext().SpanID(), peerContext.SpanID(),
		"the peer's server span is a child of the client span")
}

func TestStackedTracedTransportsRecordOneSpan(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	withGlobalTraceContext(t)
	traceparents := make(chan string, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		traceparents <- r.Header.Get("traceparent")
	}))
	t.Cleanup(peer.Close)
	ctx, caller, recorder := startCaller(t)

	get(ctx, t, PeerHTTPTransport(HTTPTransport(nil)), peer.URL, nil)
	caller.End()

	spans := clientSpans(recorder)
	require.Len(spans, 1)
	assert.Contains(<-traceparents, spans[0].SpanContext().SpanID().String(),
		"the outer transport's span and trace context win")
}

func TestHTTPTransportSkipsLongLivedStreams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			_, _ = w.Write([]byte("data: done\n\n"))
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		kind, message, err := conn.Read(r.Context())
		if err == nil {
			_ = conn.Write(r.Context(), kind, message)
		}
	}))
	t.Cleanup(server.Close)

	for _, accept := range []string{"text/event-stream", "application/json, application/x-ndjson; charset=utf-8"} {
		t.Run(accept, func(t *testing.T) {
			ctx, caller, recorder := startCaller(t)
			get(ctx, t, PeerHTTPTransport(nil), server.URL, http.Header{"Accept": {accept}})
			caller.End()
			assert.Empty(t, clientSpans(recorder))
		})
	}
	t.Run("websocket", func(t *testing.T) {
		require := require.New(t)
		ctx, caller, recorder := startCaller(t)
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{
			HTTPClient: &http.Client{Transport: PeerHTTPTransport(nil)},
		})
		require.NoError(err)
		require.NoError(conn.Write(ctx, websocket.MessageText, []byte("ping")))
		_, echoed, err := conn.Read(ctx)
		require.NoError(err)
		// Closing would end a span over the connection.
		require.NoError(conn.CloseNow())
		caller.End()
		assert.Equal(t, "ping", string(echoed))
		assert.Empty(t, clientSpans(recorder))
	})
}

type idleCloser struct {
	http.RoundTripper
	closed int
}

func (c *idleCloser) CloseIdleConnections() { c.closed++ }

func TestHTTPTransportCloseIdleConnectionsReachesBase(t *testing.T) {
	base := &idleCloser{RoundTripper: http.DefaultTransport}
	client := &http.Client{Transport: HTTPTransport(base)}

	client.CloseIdleConnections()

	assert.Equal(t, 1, base.closed)
}

func TestTraceRelayForwardsWithoutAClientSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	forwarded := make(chan string, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwarded <- string(body)
	}))
	t.Cleanup(collector.Close)
	relay := newTraceRelay(func(key string) string {
		return map[string]string{
			"OTEL_TRACES_EXPORTER":               "otlp",
			"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": collector.URL + "/v1/traces",
		}[key]
	})

	require.NoError(t, relay.Forward(t.Context(), []byte(`{"resourceSpans":[]}`)))

	assert.JSONEq(t, `{"resourceSpans":[]}`, <-forwarded)
	assert.Empty(t, recorder.Ended(), "a traced relay would add a span to every browser export")
}
