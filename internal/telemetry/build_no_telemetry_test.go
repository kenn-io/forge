//go:build kit_posthog_disabled

package telemetry

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelemetryDisabledByBuildTag(t *testing.T) {
	assert.False(t, enabledInBuild())
}

func TestBuildTagOptOutKeepsAllowlist(t *testing.T) {
	assert := assert.New(t)
	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")

	reporter, err := newReporter(Options{}, time.Now())
	require.NoError(t, err)
	handler := reporter.CaptureHandler()

	opened := postCapture(t, handler, `{"event":"app_opened"}`)
	assert.Equal(http.StatusAccepted, opened.Code)
	assert.JSONEq(`{"status":"disabled"}`, opened.Body.String())
	assert.Equal(http.StatusBadRequest, postCapture(t, handler, `{"event":"app_loaded"}`).Code)
}
