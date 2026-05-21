package server

import (
	"context"
	"strings"
)

type telemetryEventInput struct {
	Body struct {
		Event      string         `json:"event"`
		Properties map[string]any `json:"properties,omitempty"`
	}
}

type telemetryEventResponse struct {
	Status string `json:"status"`
}

type telemetryEventOutput = acceptedBodyOutput[telemetryEventResponse]

func (s *Server) captureTelemetryEvent(
	_ context.Context,
	input *telemetryEventInput,
) (*telemetryEventOutput, error) {
	event := strings.TrimSpace(input.Body.Event)
	if event == "" {
		return nil, problemBadRequest(
			CodeBadRequest, "telemetry event is required", nil,
		)
	}
	if len(event) > 120 {
		return nil, problemBadRequest(
			CodeBadRequest, "telemetry event is too long", nil,
		)
	}

	if s.telemetry == nil || !s.telemetry.Enabled() {
		return &telemetryEventOutput{
			Status: 202,
			Body:   telemetryEventResponse{Status: "disabled"},
		}, nil
	}

	if err := s.telemetry.Capture(event, input.Body.Properties); err != nil {
		return nil, problemInternal("capture telemetry event failed")
	}
	return &telemetryEventOutput{
		Status: 202,
		Body:   telemetryEventResponse{Status: "queued"},
	}, nil
}
