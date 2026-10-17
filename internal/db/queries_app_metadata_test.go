package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestDeleteAppMetadataValueReleasesClaim(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	d := openTemplateTestDB(t)
	_, err := d.GetOrCreateAppMetadataValue(t.Context(), "claim", func() (string, error) { return "first", nil })
	require.NoError(err)
	require.NoError(d.DeleteAppMetadataValue(t.Context(), " claim "))
	_, found, err := d.AppMetadataValue(t.Context(), "claim")
	require.NoError(err)
	require.False(found)
	value, err := d.GetOrCreateAppMetadataValue(t.Context(), "claim", func() (string, error) { return "retry", nil })
	require.NoError(err)
	require.Equal("retry", value)
}
