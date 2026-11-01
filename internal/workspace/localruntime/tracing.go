package localruntime

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var acpTracer = otel.Tracer("go.kenn.io/forge/internal/workspace/localruntime")

func acpSpanAttributes(info SessionInfo) trace.SpanStartEventOption {
	return trace.WithAttributes(
		attribute.String("workspace.id", info.WorkspaceID),
		attribute.String("runtime.session_key", info.Key),
	)
}

// injectTraceContext returns ctx's trace context for a process this one
// starts, so that process's start spans join this trace.
func injectTraceContext(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	return carrier
}

func extractTraceContext(ctx context.Context, traceContext map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(traceContext))
}
