//go:build middleman_no_telemetry

package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTelemetryDisabledByBuildTag(t *testing.T) {
	assert.False(t, enabledInBuild())
}
