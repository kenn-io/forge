//go:build !middleman_no_telemetry

package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTelemetryEnabledInDefaultBuild(t *testing.T) {
	assert.True(t, enabledInBuild())
}
