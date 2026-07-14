// Package tracing holds small OpenTelemetry helpers shared by the
// HTTP server and terminal attach handlers. Provider/exporter
// bootstrap lives in kit's telemetry.Init, not here.
package tracing

import (
	"context"
	"net/http"
	"net/url"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// BaggageAttributeKeys are the baggage entries copied onto server
// spans so traces are searchable by them.
var BaggageAttributeKeys = []string{"interaction", "workspace.id", "host.key"}

type queryCarrier struct{ values url.Values }

func (c queryCarrier) Get(key string) string { return c.values.Get(key) }
func (c queryCarrier) Set(string, string)    {}
func (c queryCarrier) Keys() []string {
	keys := make([]string, 0, len(c.values))
	for k := range c.values {
		keys = append(keys, k)
	}
	return keys
}

// QueryCarrier adapts URL query parameters as a propagation carrier.
// Browsers cannot set headers on WebSocket handshakes, so terminal
// attach URLs carry traceparent/baggage as query parameters instead.
func QueryCarrier(values url.Values) propagation.TextMapCarrier {
	return queryCarrier{values: values}
}

// SetBaggageAttributes copies the allow-listed baggage entries from
// ctx onto span as string attributes.
func SetBaggageAttributes(ctx context.Context, span trace.Span) {
	bag := baggage.FromContext(ctx)
	for _, key := range BaggageAttributeKeys {
		if value := bag.Member(key).Value(); value != "" {
			span.SetAttributes(attribute.String(key, value))
		}
	}
}

// StartAttachSpan extracts trace context and baggage from r's query
// parameters and starts a span. Callers must End the span before
// entering any long-lived streaming loop so attach spans stay bounded.
func StartAttachSpan(r *http.Request, name string) (context.Context, trace.Span) {
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), QueryCarrier(r.URL.Query()))
	ctx, span := otel.Tracer("go.kenn.io/middleman/internal/tracing").Start(ctx, name)
	SetBaggageAttributes(ctx, span)
	return ctx, span
}
