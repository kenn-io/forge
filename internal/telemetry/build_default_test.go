//go:build !kit_posthog_disabled

package telemetry

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelemetryEnabledInDefaultBuild(t *testing.T) {
	assert.True(t, enabledInBuild())
}

func TestNewReporterUnderGoTestWithTelemetryOn(t *testing.T) {
	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")

	reporter, err := NewReporter(Options{})
	require.NoError(t, err)

	assert.Equal(t, DisabledReporter(), reporter)
	assert.Equal(t, http.StatusBadRequest, postCapture(t, reporter.CaptureHandler(), `{"event":"app_opened"}`).Code)
}
