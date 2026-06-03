package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/posthog/posthog-go"
	"go.kenn.io/middleman/internal/db"
)

const (
	EnabledEnv           = "TELEMETRY_ENABLED"
	applicationSlug      = "middleman"
	installIDMetadataKey = "telemetry.install_id"
	postHogAPIKey        = "phc_AzHd9YvuHR7M5poKzC6eW654d3SgKyBdoQPuwkWhimUf"
	postHogEndpoint      = "https://us.i.posthog.com"
)

var ErrUnsupportedEvent = errors.New("unsupported telemetry event")

type propertyFilter func(any) (any, bool)

var allowedEvents = map[string]map[string]propertyFilter{
	"app_loaded": {
		"view": safeTelemetryToken,
	},
	"daemon_active": {
		"repo_count": safeTelemetryNumber,
	},
}

type Client interface {
	Capture(event string, properties map[string]any) error
	Close() error
	Enabled() bool
}

type Reporter struct {
	client     enqueueCloser
	distinctID string
	enabled    bool
	version    string
	commit     string
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

func EventAllowed(event string) bool {
	_, ok := allowedEvents[strings.TrimSpace(event)]
	return ok
}

func SanitizeProperties(event string, properties map[string]any) (map[string]any, error) {
	allowedProperties, ok := allowedEvents[strings.TrimSpace(event)]
	if !ok {
		return nil, ErrUnsupportedEvent
	}

	safeProperties := map[string]any{}
	for key, value := range properties {
		key = strings.TrimSpace(key)
		filter, ok := allowedProperties[key]
		if !ok {
			continue
		}
		if safeValue, ok := filter(value); ok {
			safeProperties[key] = safeValue
		}
	}
	safeProperties["$process_person_profile"] = false
	safeProperties["$geoip_disable"] = true
	safeProperties["application"] = applicationSlug
	return safeProperties, nil
}

func NewReporter(opts Options) (*Reporter, error) {
	if !EnabledFromEnv() || testing.Testing() {
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
	})
	if err != nil {
		return nil, err
	}

	return &Reporter{
		client:     client,
		distinctID: distinctID,
		enabled:    true,
		version:    opts.Version,
		commit:     opts.Commit,
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

	safeProperties, err := SanitizeProperties(event, properties)
	if err != nil {
		return err
	}

	props := posthog.Properties{}
	maps.Copy(props, safeProperties)
	r.addDefaultProperties(event, props)

	return r.client.Enqueue(posthog.Capture{
		DistinctId: r.distinctID,
		Event:      event,
		Timestamp:  time.Now().UTC(),
		Properties: props,
	})
}

func (r *Reporter) addDefaultProperties(event string, props posthog.Properties) {
	props["$process_person_profile"] = false
	props["$geoip_disable"] = true
	props["application"] = applicationSlug
	props["version"] = r.version
	props["commit"] = r.commit
	props["goos"] = runtime.GOOS
	props["goarch"] = runtime.GOARCH
	props["source"] = sourceForEvent(event)
}

func sourceForEvent(event string) string {
	if event == "daemon_active" {
		return "daemon"
	}
	return "backend"
}

func (r *Reporter) Close() error {
	if !r.Enabled() {
		return nil
	}
	return r.client.Close()
}

func safeTelemetryToken(value any) (any, bool) {
	text, ok := value.(string)
	if !ok {
		return nil, false
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 64 {
		return nil, false
	}
	for i := 0; i < len(text); i++ {
		b := text[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || b == '_' || b == '-' || b == '.' {
			continue
		}
		return nil, false
	}
	return text, true
}

func safeTelemetryNumber(value any) (any, bool) {
	switch v := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return v, true
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, false
		}
		return v, true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
		return v, true
	default:
		return nil, false
	}
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
