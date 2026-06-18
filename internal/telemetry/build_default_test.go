//go:build !kit_posthog_disabled

package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTelemetryEnabledInDefaultBuild(t *testing.T) {
	assert.True(t, enabledInBuild())
}
