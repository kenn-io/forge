package telemetryapi

import (
	telemetrypkg "go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/internal/tracing"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Telemetry telemetrypkg.Client
	// TraceRelay forwards browser spans; nil when the daemon exports no traces.
	TraceRelay *tracing.TraceRelay
}
