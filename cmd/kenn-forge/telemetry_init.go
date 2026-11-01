package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.kenn.io/forge/internal/shutdownbudget"
	oteltelemetry "go.kenn.io/kit/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

// initTelemetry starts the OpenTelemetry export the environment selects,
// naming the service unless the environment names it. The returned function
// flushes and stops the export.
func initTelemetry(ctx context.Context, service string) (func(), error) {
	var options []oteltelemetry.Option
	if os.Getenv("OTEL_SERVICE_NAME") == "" && !strings.Contains(os.Getenv("OTEL_RESOURCE_ATTRIBUTES"), "service.name=") {
		options = append(options, oteltelemetry.WithResourceOptions(resource.WithAttributes(attribute.String("service.name", service))))
	}
	shutdown, err := oteltelemetry.Init(ctx, options...)
	if err != nil {
		return nil, err
	}
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownbudget.OpenTelemetry)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			slog.Warn("telemetry shutdown failed", "err", err)
		}
	}, nil
}
