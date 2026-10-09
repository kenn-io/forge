package telemetryapi

import (
	"context"
	"log/slog"
	"strings"
	"time"

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

	properties := input.Body.Properties
	var screen, claim string
	if event == "screen_viewed" {
		var claimed bool
		var err error
		screen, claim, claimed, err = s.claimScreenView(ctx, properties)
		if err != nil {
			return nil, httpapi.Internal("capture telemetry event failed")
		}
		if !claimed {
			return &telemetryEventOutput{
				Status: 202,
				Body:   TelemetryEventResponse{Status: "skipped"},
			}, nil
		}
		properties = map[string]any{"screen": screen, "surface": properties["surface"]}
	}

	// The reporter drops properties its allowlist omits.
	if err := s.Telemetry.Capture(event, properties); err != nil {
		if claim != "" {
			s.releaseScreenView(ctx, screen, claim)
		}
		return nil, httpapi.Internal("capture telemetry event failed")
	}
	return &telemetryEventOutput{
		Status: 202,
		Body:   TelemetryEventResponse{Status: "queued"},
	}, nil
}

// claimScreenView reports claimed=false for unknown screens and for screens
// this installation already reported today.
func (s *Handlers) claimScreenView(
	ctx context.Context,
	properties map[string]any,
) (screen, claim string, claimed bool, err error) {
	screen, valid := telemetrypkg.ScreenName(properties["screen"])
	if !valid {
		return "", "", false, nil
	}
	day := (*s.Now)().UTC().Format(time.DateOnly)
	claim, claimed, err = s.DB.ClaimTelemetryScreenDay(
		ctx, telemetrypkg.InstallIDMetadataKey, screen, day,
	)
	return screen, claim, claimed, err
}

// releaseScreenView lets a later visit report the screen after the queue rejected it.
func (s *Handlers) releaseScreenView(ctx context.Context, screen, claim string) {
	if err := s.DB.ReleaseTelemetryScreenDay(context.WithoutCancel(ctx), screen, claim); err != nil {
		slog.Warn("telemetry screen claim release failed", "screen", screen, "err", err)
	}
}
