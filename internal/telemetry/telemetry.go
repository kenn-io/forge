package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/kit/telemetry/posthog"
)

const (
	EnabledEnv           = "TELEMETRY_ENABLED"
	applicationSlug      = "kenn-forge"
	envPrefix            = "KENN_FORGE"
	InstallIDMetadataKey = "telemetry.install_id"
	installedAtKey       = "telemetry.install_created_at"
	postHogAPIKey        = "phc_AzHd9YvuHR7M5poKzC6eW654d3SgKyBdoQPuwkWhimUf"
	postHogEndpoint      = "https://us.i.posthog.com"
)

// HeartbeatInterval is how often a running daemon reports daemon_active.
const HeartbeatInterval = 24 * time.Hour

var ErrUnsupportedEvent = errors.New("unsupported telemetry event")

var screenFilter = posthog.AllowStringValues(
	"activity", "actions", "repos", "repo-browser", "pulls", "issues", "docs",
	"workspaces", "terminal", "workspace-item", "settings", "project-intake",
	"design-system", "onboarding",
)

var durationFilter = posthog.AllowStringValues("under_1m", "1_to_5m", "5_to_30m", "over_30m", "30m_to_2h", "over_2h")

func SessionDuration(value any) (any, bool) {
	return durationFilter(value)
}

// ScreenName uses the reporter's allowlist for the daily claim too.
func ScreenName(value any) (string, bool) {
	filtered, ok := screenFilter(value)
	name, _ := filtered.(string)
	return name, ok
}

var allowedEvents = map[string]map[string]posthog.PropertyFilter{
	"screen_viewed": {
		"screen":  screenFilter,
		"surface": posthog.AllowStringValues("web"),
	},
	"app_opened": {
		"surface": posthog.AllowStringValues("web"),
	},
	"session_ended": {
		"surface":         posthog.AllowStringValues("web"),
		"duration_bucket": durationFilter,
	},
	"daemon_active": {
		"repo_count": posthog.AllowNumber,
	},
}

type Client = posthog.Client

// Reporter routes each event to the kit reporter for its source, since kit
// fixes the source property per reporter.
type Reporter struct {
	daemon  posthog.Client
	backend posthog.Client
}

type Options struct {
	Database *db.DB
	Version  string
	Commit   string
}

// newKitReporter builds one kit reporter; tests replace it.
var newKitReporter = func(opts posthog.Options, options ...posthog.Option) (posthog.Client, error) {
	return posthog.NewReporter(opts, options...)
}

// EnabledFromEnv reports whether the environment allows telemetry. Kit honors
// both the documented generic TELEMETRY_ENABLED=0 opt-out and the prefixed
// KENN_FORGE_TELEMETRY_ENABLED=0 one.
func EnabledFromEnv() bool {
	return posthog.EnabledFromEnv(envPrefix)
}

func EventAllowed(event string) bool {
	_, ok := allowedEvents[strings.TrimSpace(event)]
	return ok
}

// UIEventAllowed reports whether the web UI may send event; daemon events stay daemon-only.
func UIEventAllowed(event string) bool {
	return EventAllowed(event) && sourceForEvent(strings.TrimSpace(event)) == "backend"
}

func NewReporter(opts Options) (*Reporter, error) {
	if testing.Testing() {
		return DisabledReporter(), nil
	}
	return newReporter(opts, time.Now())
}

// newReporter is NewReporter without the go test guard.
func newReporter(opts Options, now time.Time) (*Reporter, error) {
	if !enabledInBuild() || !EnabledFromEnv() {
		return DisabledReporter(), nil
	}
	if opts.Database == nil {
		return nil, errors.New("telemetry database is required")
	}

	distinctID, installedAt, err := loadOrCreateInstallID(context.Background(), opts.Database, now)
	if err != nil {
		return nil, err
	}

	base := posthog.Options{
		APIKey:      postHogAPIKey,
		Endpoint:    postHogEndpoint,
		Application: applicationSlug,
		EnvPrefix:   envPrefix,
		DistinctID:  distinctID,
		Version:     opts.Version,
		Commit:      opts.Commit,
		InstalledAt: installedAt,
	}
	daemonOpts := base
	daemonOpts.Source = "daemon"
	daemon, err := newKitReporter(daemonOpts, kitAllowedEvents("daemon")...)
	if err != nil {
		return nil, err
	}
	backendOpts := base
	backendOpts.Source = "backend"
	backend, err := newKitReporter(backendOpts, kitAllowedEvents("backend")...)
	if err != nil {
		return nil, errors.Join(err, daemon.Close())
	}
	return &Reporter{daemon: daemon, backend: backend}, nil
}

// kitAllowedEvents builds kit's allowlist for one source from allowedEvents.
func kitAllowedEvents(source string) []posthog.Option {
	var options []posthog.Option
	for event, properties := range allowedEvents {
		if sourceForEvent(event) != source {
			continue
		}
		allowed := make([]posthog.AllowedProperty, 0, len(properties))
		for name, filter := range properties {
			allowed = append(allowed, posthog.AllowProperty(name, filter))
		}
		options = append(options, posthog.WithAllowedEvent(event, allowed...))
	}
	return options
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
	if r == nil {
		return false
	}
	return (r.daemon != nil && r.daemon.Enabled()) || (r.backend != nil && r.backend.Enabled())
}

func (r *Reporter) Capture(event string, properties map[string]any) error {
	if !r.Enabled() {
		return nil
	}

	event = strings.TrimSpace(event)
	if event == "" {
		return errors.New("telemetry event is required")
	}
	if !EventAllowed(event) {
		return ErrUnsupportedEvent
	}

	client := r.backend
	if sourceForEvent(event) == "daemon" {
		client = r.daemon
	}
	if client == nil {
		return nil
	}
	return client.Capture(event, properties)
}

func sourceForEvent(event string) string {
	if event == "daemon_active" {
		return "daemon"
	}
	return "backend"
}

func (r *Reporter) Close() error {
	if r == nil {
		return nil
	}
	var err error
	for _, client := range []posthog.Client{r.daemon, r.backend} {
		if client != nil {
			err = errors.Join(err, client.Close())
		}
	}
	return err
}

// loadOrCreateInstallID returns the install ID and when it was created. A zero
// time means the age is unknown (the ID predates install-age tracking or the
// stored time is unreadable), so events go untagged.
func loadOrCreateInstallID(ctx context.Context, database *db.DB, now time.Time) (string, time.Time, error) {
	_, found, err := database.AppMetadataValue(ctx, InstallIDMetadataKey)
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
	id, err := database.GetOrCreateAppMetadataValue(ctx, InstallIDMetadataKey, randomInstallID)
	if err != nil {
		return "", time.Time{}, err
	}
	raw, found, err := database.AppMetadataValue(ctx, installedAtKey)
	if err != nil || !found {
		return id, time.Time{}, err
	}
	installedAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		slog.Warn("telemetry install age unavailable", "err", err)
		return id, time.Time{}, nil
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
