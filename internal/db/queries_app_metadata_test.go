package db

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelemetryScreenDayClaimsPersist(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "test.db")
	copyTestDBTemplate(t, testDBTemplateBytes(t), path)
	d, err := OpenPreparedForTest(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(d.Close()) })
	other, err := OpenPreparedForTest(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(other.Close()) })
	_, err = d.GetOrCreateAppMetadataValue(t.Context(), "telemetry.install_id", func() (string, error) { return "install-a", nil })
	require.NoError(err)
	var count atomic.Int32
	var wg sync.WaitGroup
	claimErrs := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() {
			database := d
			if i%2 == 0 {
				database = other
			}
			_, claimed, err := database.ClaimTelemetryScreenDay(t.Context(), "telemetry.install_id", "activity", "2026-01-02")
			claimErrs <- err
			if claimed {
				count.Add(1)
			}
		})
	}
	wg.Wait()
	close(claimErrs)
	for err := range claimErrs {
		require.NoError(err)
	}
	assert.Equal(int32(1), count.Load())
	for _, test := range []struct {
		screen, day string
		claimed     bool
	}{
		{"activity", "2026-01-02", false},
		{"docs", "2026-01-02", true},
		{"activity", "2026-01-03", true},
	} {
		_, claimed, err := other.ClaimTelemetryScreenDay(t.Context(), "telemetry.install_id", test.screen, test.day)
		require.NoError(err)
		assert.Equal(test.claimed, claimed)
	}
	require.NoError(d.ReleaseTelemetryScreenDay(t.Context(), "activity", "install-a\n2026-01-02"))
	_, claimed, err := d.ClaimTelemetryScreenDay(t.Context(), "telemetry.install_id", "activity", "2026-01-03")
	require.NoError(err)
	assert.False(claimed)
	_, err = d.rwExecContext(t.Context(), `UPDATE forge_app_metadata SET value = ? WHERE key = 'telemetry.install_id'`, "install-b")
	require.NoError(err)
	claim, claimed, err := other.ClaimTelemetryScreenDay(t.Context(), "telemetry.install_id", "activity", "2026-01-03")
	require.NoError(err)
	assert.True(claimed)
	require.NoError(d.ReleaseTelemetryScreenDay(t.Context(), "activity", claim))
	_, claimed, err = other.ClaimTelemetryScreenDay(t.Context(), "telemetry.install_id", "activity", "2026-01-03")
	require.NoError(err)
	assert.True(claimed)
}

func TestGetOrCreateAppMetadataValueCreatesAndReusesValue(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	d := openTemplateTestDB(t)
	ctx := t.Context()

	value, err := d.GetOrCreateAppMetadataValue(ctx, "telemetry.install_id", func() (string, error) {
		return "first-id", nil
	})
	require.NoError(err)
	assert.Equal("first-id", value)

	value, err = d.GetOrCreateAppMetadataValue(ctx, "telemetry.install_id", func() (string, error) {
		return "second-id", nil
	})
	require.NoError(err)
	assert.Equal("first-id", value)

	stored, found, err := d.AppMetadataValue(ctx, "telemetry.install_id")
	require.NoError(err)
	assert.True(found)
	assert.Equal("first-id", stored)
}

func TestAppMetadataValueReturnsNotFound(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	d := openTemplateTestDB(t)

	value, found, err := d.AppMetadataValue(t.Context(), "missing")
	require.NoError(err)
	assert.False(found)
	assert.Empty(value)
}
