package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/testutil"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestHTTPSpansParentedOnTraceparent(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		otel.SetTextMapPropagator(prevProp)
	})

	database := dbtest.Open(t)
	_, err := testutil.SeedFixtures(t.Context(), database)
	require.NoError(err)
	srv := New(database, nil, nil, "/", nil, ServerOptions{})

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("traceparent", "00-33333333333333333333333333333333-4444444444444444-01")
	req.Header.Set("baggage", "interaction=workspace-switch,workspace.id=ws-9")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var got sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if s.SpanContext().TraceID().String() == "33333333333333333333333333333333" {
			got = s
		}
	}
	require.NotNil(got, "no span recorded for the injected trace id")
	assert.Equal("4444444444444444", got.Parent().SpanID().String())
	assert.Contains(got.Name(), "GET ")
	attrs := map[string]string{}
	for _, kv := range got.Attributes() {
		if kv.Value.Type() == attribute.STRING {
			attrs[string(kv.Key)] = kv.Value.AsString()
		}
	}
	assert.Equal("ws-9", attrs["workspace.id"])
}

func TestHTTPSpanUsesMatchedRouteUnderBasePath(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	database := dbtest.Open(t)
	srv := New(database, nil, nil, "/middleman/", nil, ServerOptions{})

	req := httptest.NewRequest(http.MethodGet, "/middleman/api/v1/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	spanNames := make([]string, 0, len(recorder.Ended()))
	for _, span := range recorder.Ended() {
		spanNames = append(spanNames, span.Name())
	}
	assert.Contains(t, spanNames, "GET /api/v1/sync/status")
}

func TestOTelTraceableFiltersOnlyLongLivedStreams(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		method   string
		target   string
		upgrade  bool
		want     bool
	}{
		{name: "websocket", basePath: "/", method: http.MethodGet, target: "/ws/v1/terminal", upgrade: true, want: false},
		{name: "server events", basePath: "/", method: http.MethodGet, target: "/api/v1/events", want: false},
		{name: "prefixed server events", basePath: "/middleman/", method: http.MethodGet, target: "/middleman/api/v1/events", want: false},
		{name: "telemetry event", basePath: "/", method: http.MethodPost, target: "/api/v1/telemetry/events", want: true},
		{name: "kata events stream", method: http.MethodGet, target: "/api/v1/kata/proxy/api/v1/events/stream", want: false},
		{name: "kata events page", method: http.MethodGet, target: "/api/v1/kata/proxy/api/v1/events?limit=1000", want: true},
		{name: "roborev event stream", method: http.MethodGet, target: "/api/roborev/api/stream/events", want: false},
		{name: "roborev job output stream", method: http.MethodGet, target: "/api/roborev/api/job/output?job_id=7&stream=1", want: false},
		{name: "roborev job output snapshot", method: http.MethodGet, target: "/api/roborev/api/job/output?job_id=7", want: true},
		{name: "roborev raw job log", method: http.MethodGet, target: "/api/roborev/api/job/log?job_id=7", want: true},
		{name: "roborev sync stream", method: http.MethodPost, target: "/api/roborev/api/sync/now?stream=1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, nil)
			if tt.upgrade {
				req.Header.Set("Upgrade", "websocket")
			}
			assert.Equal(t, tt.want, otelTraceable(tt.basePath)(req))
		})
	}
}
