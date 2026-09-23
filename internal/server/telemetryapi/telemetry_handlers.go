package telemetryapi

import (
	"context"
	"errors"
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
	if !telemetrypkg.EventAllowed(event) {
		return nil, httpapi.BadRequest(
			httpapi.CodeBadRequest, "unsupported telemetry event", nil,
		)
	}

	safeProperties, err := telemetrypkg.SanitizeProperties(
		event, input.Body.Properties,
	)
	if err != nil {
		if errors.Is(err, telemetrypkg.ErrUnsupportedEvent) {
			return nil, httpapi.BadRequest(
				httpapi.CodeBadRequest, "unsupported telemetry event", nil,
			)
		}
		return nil, httpapi.Internal("sanitize telemetry event failed")
	}

	if s.Telemetry == nil || !s.Telemetry.Enabled() {
		return &telemetryEventOutput{
			Status: 202,
			Body:   TelemetryEventResponse{Status: "disabled"},
		}, nil
	}

	if err := s.Telemetry.Capture(event, safeProperties); err != nil {
		if errors.Is(err, telemetrypkg.ErrUnsupportedEvent) {
			return nil, httpapi.BadRequest(
				httpapi.CodeBadRequest, "unsupported telemetry event", nil,
			)
		}
		return nil, httpapi.Internal("capture telemetry event failed")
	}
	return &telemetryEventOutput{
		Status: 202,
		Body:   TelemetryEventResponse{Status: "queued"},
	}, nil
}
