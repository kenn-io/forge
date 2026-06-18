//go:build kit_posthog_disabled

package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTelemetryDisabledByBuildTag(t *testing.T) {
	assert.False(t, enabledInBuild())
}
