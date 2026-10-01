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
	"sync"
	"testing"
	"time"

	"github.com/posthog/posthog-go"
	"go.kenn.io/forge/internal/db"
)

const (
	EnabledEnv           = "TELEMETRY_ENABLED"
	applicationSlug      = "kenn-forge"
	installIDMetadataKey = "telemetry.install_id"
	installedAtKey       = "telemetry.install_created_at"
	postHogAPIKey        = "phc_AzHd9YvuHR7M5poKzC6eW654d3SgKyBdoQPuwkWhimUf"
	postHogEndpoint      = "https://us.i.posthog.com"
)

const (
	// HeartbeatInterval is how often a running daemon reports daemon_active.
	HeartbeatInterval = 24 * time.Hour
	// Throwaway installs (sandboxes, test harnesses) never live this long.
	maturityAge   = 24 * time.Hour
	maxHeldEvents = 1000
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
	// installedAt is zero for installs created before install age was recorded.
	installedAt time.Time
	now         func() time.Time

	mu         sync.Mutex
	held       []posthog.Capture
	flushTimer *time.Timer
	closed     bool
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
	if !enabledInBuild() || !EnabledFromEnv() || testing.Testing() {
		return DisabledReporter(), nil
	}
	if opts.Database == nil {
		return nil, errors.New("telemetry database is required")
	}

	distinctID, installedAt, err := loadOrCreateInstallID(context.Background(), opts.Database, time.Now())
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

	reporter := &Reporter{
		client:      client,
		distinctID:  distinctID,
		enabled:     true,
		version:     opts.Version,
		commit:      opts.Commit,
		installedAt: installedAt,
	}
	reporter.scheduleMaturityFlush()
	return reporter, nil
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

	return r.send(posthog.Capture{
		DistinctId: r.distinctID,
		Event:      event,
		Timestamp:  r.clock().UTC(),
		Properties: props,
	})
}

// send holds events until the install is old enough to count, then sends the
// held events with their original capture times ahead of the new one.
func (r *Reporter) send(capture posthog.Capture) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.matureAt(capture.Timestamp) {
		if len(r.held) < maxHeldEvents {
			r.held = append(r.held, capture)
		}
		return nil
	}
	return errors.Join(r.flushHeldLocked(), r.client.Enqueue(capture))
}

// scheduleMaturityFlush sends held events when the install turns 24 hours old,
// so they don't wait for the next capture.
func (r *Reporter) scheduleMaturityFlush() {
	if r.installedAt.IsZero() {
		return
	}
	wait := r.installedAt.Add(maturityAge).Sub(r.clock())
	if wait <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushTimer = time.AfterFunc(wait, r.flushIfMature)
}

func (r *Reporter) flushIfMature() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !r.matureAt(r.clock()) {
		return
	}
	if err := r.flushHeldLocked(); err != nil {
		slog.Warn("send held telemetry events", "err", err)
	}
}

func (r *Reporter) flushHeldLocked() error {
	var err error
	for _, capture := range r.held {
		err = errors.Join(err, r.client.Enqueue(capture))
	}
	r.held = nil
	return err
}

func (r *Reporter) matureAt(t time.Time) bool {
	return r.installedAt.IsZero() || t.Sub(r.installedAt) >= maturityAge
}

func (r *Reporter) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
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
	r.mu.Lock()
	if r.flushTimer != nil {
		r.flushTimer.Stop()
	}
	var flushErr error
	if r.matureAt(r.clock()) {
		flushErr = r.flushHeldLocked()
	}
	// Events from an install that closes before maturity are dropped.
	r.held = nil
	r.closed = true
	r.mu.Unlock()
	return errors.Join(flushErr, r.client.Close())
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
	for i := range len(text) {
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

// loadOrCreateInstallID returns the install ID and when it was created. A zero
// time means the ID predates install-age tracking and counts as mature.
func loadOrCreateInstallID(ctx context.Context, database *db.DB, now time.Time) (string, time.Time, error) {
	_, found, err := database.AppMetadataValue(ctx, installIDMetadataKey)
	if err != nil {
		return "", time.Time{}, err
	}
	if !found {
		// Write the creation time first so a crash between writes can't leave a new ID that looks old.
		if _, err := database.GetOrCreateAppMetadataValue(ctx, installedAtKey, func() (string, error) {
			return now.UTC().Format(time.RFC3339Nano), nil
		}); err != nil {
			return "", time.Time{}, err
		}
	}
	id, err := database.GetOrCreateAppMetadataValue(ctx, installIDMetadataKey, randomInstallID)
	if err != nil {
		return "", time.Time{}, err
	}
	raw, found, err := database.AppMetadataValue(ctx, installedAtKey)
	if err != nil || !found {
		return id, time.Time{}, err
	}
	installedAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parse telemetry install time: %w", err)
	}
	return id, installedAt, nil
}

func randomInstallID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate telemetry install id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
