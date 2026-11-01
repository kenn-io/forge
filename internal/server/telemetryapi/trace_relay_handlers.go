package telemetryapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/httpapi"
)

// MaxBrowserTraceBatchBytes bounds one browser OTLP JSON trace batch.
const MaxBrowserTraceBatchBytes = 4 << 20

type browserTracesInput struct {
	RawBody []byte `contentType:"application/json"`
}

// RegisterBrowserTraces registers the relay that forwards browser spans to
// the daemon's trace collector.
func (s *Handlers) RegisterBrowserTraces(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "export-browser-traces",
		Method:        http.MethodPost,
		Path:          "/telemetry/traces",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Forward browser OpenTelemetry spans to the daemon's trace collector",
		Tags:          []string{"System"},
		MaxBodyBytes:  MaxBrowserTraceBatchBytes,
		// The batch is opaque OTLP JSON; the collector validates it.
		SkipValidateBody: true,
	}, s.ExportBrowserTraces)
}

func (s *Handlers) ExportBrowserTraces(ctx context.Context, input *browserTracesInput) (*struct{}, error) {
	if s.TraceRelay == nil {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "the daemon does not export traces", nil)
	}
	if err := s.TraceRelay.Forward(ctx, input.RawBody); err != nil {
		slog.Debug("forward browser traces", "err", err)
		return nil, httpapi.NewProblem(http.StatusBadGateway, httpapi.CodeUpstreamError, "trace collector unavailable", nil)
	}
	return nil, nil
}
