//go:build kit_posthog_disabled

package telemetry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/telemetry/posthog"
)

func TestTelemetryDisabledByBuildTag(t *testing.T) {
	assert.False(t, enabledInBuild())
	reporter, err := newReporter(Options{}, time.Now())
	require.NoError(t, err)
	require.False(t, reporter.Enabled())
	_, err = reporter.Report(t.Context(), "session_ended", nil)
	require.ErrorIs(t, err, posthog.ErrInvalidProperty)
	status, err := reporter.Report(t.Context(), "session_ended", map[string]any{"duration_bucket": "under_1m"})
	require.NoError(t, err)
	assert.Equal(t, posthog.StatusDisabled, status)
}
