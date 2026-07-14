package tracing

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestStartAttachSpanExtractsQueryParamContext(t *testing.T) {
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

	r := httptest.NewRequest("GET",
		"/ws/v1/workspaces/abc/terminal"+
			"?traceparent=00-11111111111111111111111111111111-2222222222222222-01"+
			"&baggage=interaction%3Dworkspace-switch%2Cworkspace.id%3Dabc",
		nil)
	_, span := StartAttachSpan(r, "terminal.attach")
	span.End()

	spans := recorder.Ended()
	require.Len(spans, 1)
	assert.Equal("terminal.attach", spans[0].Name())
	assert.Equal("11111111111111111111111111111111", spans[0].SpanContext().TraceID().String())
	assert.Equal("2222222222222222", spans[0].Parent().SpanID().String())
	attrs := map[string]string{}
	for _, kv := range spans[0].Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal("workspace-switch", attrs["interaction"])
	assert.Equal("abc", attrs["workspace.id"])
}

func TestStartAttachSpanWithoutParamsStartsRootSpan(t *testing.T) {
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

	r := httptest.NewRequest("GET", "/ws/v1/workspaces/abc/terminal", nil)
	_, span := StartAttachSpan(r, "terminal.attach")
	span.End()

	spans := recorder.Ended()
	require.Len(spans, 1)
	assert.Equal("terminal.attach", spans[0].Name())
	assert.False(spans[0].Parent().IsValid())
	attrs := map[string]string{}
	for _, kv := range spans[0].Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Empty(attrs["interaction"])
}
