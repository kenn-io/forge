package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/wesm/middleman/internal/db"
)

const (
	EnabledEnv           = "TELEMETRY_ENABLED"
	installIDMetadataKey = "telemetry.install_id"
	postHogAPIKey        = "phc_AzHd9YvuHR7M5poKzC6eW654d3SgKyBdoQPuwkWhimUf"
	postHogEndpoint      = "https://us.i.posthog.com"
)

type Client interface {
	Capture(event string, properties map[string]any) error
	Close() error
	Enabled() bool
}

type Reporter struct {
	client     enqueueCloser
	distinctID string
	enabled    bool
}

type enqueueCloser interface {
	Enqueue(posthog.Message) error
	Close() error
}

type Options struct {
	Database *db.DB
	Version  string
	Commit   string
}

func EnabledFromEnv() bool {
	return strings.TrimSpace(os.Getenv(EnabledEnv)) != "0"
}

func NewReporter(opts Options) (*Reporter, error) {
	if !EnabledFromEnv() {
		return DisabledReporter(), nil
	}
	if opts.Database == nil {
		return nil, errors.New("telemetry database is required")
	}

	distinctID, err := loadOrCreateInstallID(context.Background(), opts.Database)
	if err != nil {
		return nil, err
	}

	disableGeoIP := true
	client, err := posthog.NewWithConfig(postHogAPIKey, posthog.Config{
		Endpoint:     postHogEndpoint,
		DisableGeoIP: &disableGeoIP,
		DefaultEventProperties: posthog.Properties{
			"app":            "middleman",
			"source":         "backend",
			"version":        opts.Version,
			"commit":         opts.Commit,
			"goos":           runtime.GOOS,
			"goarch":         runtime.GOARCH,
			"$geoip_disable": true,
		},
	})
	if err != nil {
		return nil, err
	}

	return &Reporter{
		client:     client,
		distinctID: distinctID,
		enabled:    true,
	}, nil
}

func DisabledReporter() *Reporter {
	return &Reporter{}
}

func NewReporterOrDisabled(opts Options) *Reporter {
	reporter, err := NewReporter(opts)
	if err != nil {
		slog.Warn("telemetry disabled", "err", err)
		return DisabledReporter()
	}
	return reporter
}

func (r *Reporter) Enabled() bool {
	return r != nil && r.enabled && r.client != nil
}

func (r *Reporter) Capture(event string, properties map[string]any) error {
	if !r.Enabled() {
		return nil
	}

	event = strings.TrimSpace(event)
	if event == "" {
		return errors.New("telemetry event is required")
	}

	props := posthog.Properties{
		"$geoip_disable": true,
	}
	for key, value := range properties {
		key = strings.TrimSpace(key)
		if key == "" || key == "distinct_id" || key == "distinctId" {
			continue
		}
		props[key] = value
	}

	return r.client.Enqueue(posthog.Capture{
		DistinctId: r.distinctID,
		Event:      event,
		Timestamp:  time.Now().UTC(),
		Properties: props,
	})
}

func (r *Reporter) Close() error {
	if !r.Enabled() {
		return nil
	}
	return r.client.Close()
}

func loadOrCreateInstallID(ctx context.Context, database *db.DB) (string, error) {
	return database.GetOrCreateAppMetadataValue(ctx, installIDMetadataKey, randomInstallID)
}

func randomInstallID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate telemetry install id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
