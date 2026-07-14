package server

import (
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
