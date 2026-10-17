//go:build !kit_posthog_disabled

package telemetry

import (
	"net/http"
	"testing"
	"time"
)

// SetupDailyReporter builds a real reporter that sends to a test collector.
func SetupDailyReporter(t *testing.T, opts Options, now time.Time, handler http.Handler) *Reporter {
	t.Helper()
	return setupDailyReporter(t, opts, now, handler)
}
