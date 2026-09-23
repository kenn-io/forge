package otelmiddleware

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/tracing"
	"go.opentelemetry.io/otel/trace"
)

// otelSpanName formats the span name for otelhttp. otelhttp renames
// its span a second time after the handler returns, using this
// formatter, whenever the stdlib mux recorded a matched pattern on
// the request (r.Pattern); that second call would otherwise clobber
// the route-pattern name set by otelSpanMiddleware with the generic
// "forge.http" operation name. r.Pattern is exactly the
// method-prefixed pattern Huma registered (e.g. "GET /healthz"), so
// prefer it when present and fall back to the static operation name
// for non-Huma handlers (SPA assets, the roborev proxy).
func OtelSpanName(operation string, r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return operation
}

// stripPrefixPreservingPattern lets an inner ServeMux match against a
// stripped path while copying that matched route back to the request
// retained by outer middleware such as otelhttp.
func StripPrefixPreservingPattern(prefix string, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix(prefix, http.HandlerFunc(func(w http.ResponseWriter, stripped *http.Request) {
			handler.ServeHTTP(w, stripped)
			if stripped.Pattern != "" {
				r.Pattern = stripped.Pattern
			}
		})).ServeHTTP(w, r)
	})
}

// otelSpanMiddleware renames the otelhttp-created span to the matched
// Huma route pattern (otelhttp cannot see it at span start) and copies
// allow-listed baggage onto it.
func OtelSpanMiddleware(ctx huma.Context, next func(huma.Context)) {
	span := trace.SpanFromContext(ctx.Context())
	if span.IsRecording() {
		if op := ctx.Operation(); op != nil {
			span.SetName(ctx.Method() + " " + op.Path)
		}
		tracing.SetBaggageAttributes(ctx.Context(), span)
	}
	next(ctx)
}
