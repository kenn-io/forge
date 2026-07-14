package server

import (
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/otel/trace"

	"go.kenn.io/middleman/internal/tracing"
)

// otelTraceable reports whether a request should get an otelhttp
// server span. WebSocket upgrades and the SSE events stream live for
// the connection lifetime and would produce hours-long spans;
// terminal attach gets its own bounded span instead (internal/tracing).
func otelTraceable(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	return !strings.HasSuffix(r.URL.Path, "/events")
}

// otelSpanName formats the span name for otelhttp. otelhttp renames
// its span a second time after the handler returns, using this
// formatter, whenever the stdlib mux recorded a matched pattern on
// the request (r.Pattern); that second call would otherwise clobber
// the route-pattern name set by otelSpanMiddleware with the generic
// "middleman.http" operation name. r.Pattern is exactly the
// method-prefixed pattern Huma registered (e.g. "GET /healthz"), so
// prefer it when present and fall back to the static operation name
// for non-Huma handlers (SPA assets, the roborev proxy).
func otelSpanName(operation string, r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return operation
}

// otelSpanMiddleware renames the otelhttp-created span to the matched
// Huma route pattern (otelhttp cannot see it at span start) and copies
// allow-listed baggage onto it.
func otelSpanMiddleware(ctx huma.Context, next func(huma.Context)) {
	span := trace.SpanFromContext(ctx.Context())
	if span.IsRecording() {
		if op := ctx.Operation(); op != nil {
			span.SetName(ctx.Method() + " " + op.Path)
		}
		tracing.SetBaggageAttributes(ctx.Context(), span)
	}
	next(ctx)
}
