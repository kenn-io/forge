package telemetryapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/server/httpapi"
	telemetrypkg "go.kenn.io/forge/internal/telemetry"
)

type telemetryEventInput struct {
	Body struct {
		Event      string         `json:"event"`
		Properties map[string]any `json:"properties,omitempty"`
	}
}

type TelemetryEventResponse struct {
	Status string `json:"status"`
}

type telemetryEventOutput = httpapi.AcceptedBodyOutput[TelemetryEventResponse]

func (s *Handlers) CaptureTelemetryEvent(
	_ context.Context,
	input *telemetryEventInput,
) (*telemetryEventOutput, error) {
	event := strings.TrimSpace(input.Body.Event)
	if event == "" {
		return nil, httpapi.BadRequest(
			httpapi.CodeBadRequest, "telemetry event is required", nil,
		)
	}
	if len(event) > 120 {
		return nil, httpapi.BadRequest(
			httpapi.CodeBadRequest, "telemetry event is too long", nil,
		)
	}
	if !telemetrypkg.UIEventAllowed(event) {
		return nil, httpapi.BadRequest(
			httpapi.CodeBadRequest, "unsupported telemetry event", nil,
		)
	}

	if s.Telemetry == nil || !s.Telemetry.Enabled() {
		return &telemetryEventOutput{
			Status: 202,
			Body:   TelemetryEventResponse{Status: "disabled"},
		}, nil
	}

	// The reporter drops properties its allowlist omits.
	if err := s.Telemetry.Capture(event, input.Body.Properties); err != nil {
		return nil, httpapi.Internal("capture telemetry event failed")
	}
	return &telemetryEventOutput{
		Status: 202,
		Body:   TelemetryEventResponse{Status: "queued"},
	}, nil
}
