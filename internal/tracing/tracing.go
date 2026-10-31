// Package tracing holds small OpenTelemetry helpers shared by the
// HTTP server and terminal attach handlers. Provider/exporter
// bootstrap lives in kit's telemetry.Init, not here.
package tracing

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// EndSpan ends span, marking it failed when err is not nil.
func EndSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// StartSubprocess starts a span for one subprocess run, including any wait
// for subprocess capacity. Arguments can carry credentials, so the span names
// only the executable and, for git, its subcommand.
func StartSubprocess(ctx context.Context, args []string) (context.Context, trace.Span) {
	name, subcommand := subprocessName(args)
	attrs := []attribute.KeyValue{attribute.String("process.executable.name", name)}
	spanName := "exec " + name
	if subcommand != "" {
		attrs = append(attrs, attribute.String("git.subcommand", subcommand))
		spanName += " " + subcommand
	}
	return otel.Tracer("go.kenn.io/forge/internal/tracing").Start(ctx, spanName, trace.WithAttributes(attrs...))
}

func subprocessName(args []string) (string, string) {
	if len(args) == 0 {
		return "unknown", ""
	}
	name := strings.TrimSuffix(filepath.Base(args[0]), ".exe")
	if name != "git" {
		return name, ""
	}
	for i := 1; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-c" || arg == "-C":
			i++ // skip the option's value
		case strings.HasPrefix(arg, "-"):
		default:
			return name, arg
		}
	}
	return name, ""
}

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
	ctx, span := otel.Tracer("go.kenn.io/forge/internal/tracing").Start(ctx, name)
	SetBaggageAttributes(ctx, span)
	return ctx, span
}
