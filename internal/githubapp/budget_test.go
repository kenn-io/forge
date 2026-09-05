package githubapp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func appTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func appTestMeter(t *testing.T) *platform.Meter {
	t.Helper()
	meter, err := platform.NewMeter(appTestContext(t), platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
	require.NoError(t, err)
	return meter
}
