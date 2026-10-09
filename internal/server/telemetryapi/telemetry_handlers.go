package telemetryapi

import (
	"context"
	"errors"
	"strings"

	"go.kenn.io/forge/internal/server/httpapi"
	telemetrypkg "go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/kit/telemetry/posthog"
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
	ctx context.Context,
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
	if event == "session_ended" {
		if _, valid := telemetrypkg.SessionDuration(input.Body.Properties["duration_bucket"]); !valid {
			return nil, httpapi.BadRequest(httpapi.CodeBadRequest, "unsupported or missing session duration", nil)
		}
	}

	if s.Telemetry == nil || !s.Telemetry.Enabled() {
		return &telemetryEventOutput{
			Status: 202,
			Body:   TelemetryEventResponse{Status: "disabled"},
		}, nil
	}

	status, err := s.Telemetry.Report(ctx, event, input.Body.Properties)
	if errors.Is(err, posthog.ErrInvalidProperty) {
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, "unsupported or missing telemetry property", nil)
	}
	if err != nil {
		return nil, httpapi.Internal("capture telemetry event failed")
	}
	return &telemetryEventOutput{
		Status: 202,
		Body:   TelemetryEventResponse{Status: string(status)},
	}, nil
}
