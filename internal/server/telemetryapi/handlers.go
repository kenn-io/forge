package telemetryapi

import (
	"time"

	"go.kenn.io/forge/internal/db"
	telemetrypkg "go.kenn.io/forge/internal/telemetry"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Telemetry telemetrypkg.Client
	DB        *db.DB
	Now       *func() time.Time
}
