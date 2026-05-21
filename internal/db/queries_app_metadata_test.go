package db

import (
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetOrCreateAppMetadataValueCreatesAndReusesValue(t *testing.T) {
	assert := Assert.New(t)
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
	assert := Assert.New(t)
	require := require.New(t)

	d := openTemplateTestDB(t)

	value, found, err := d.AppMetadataValue(t.Context(), "missing")
	require.NoError(err)
	assert.False(found)
	assert.Empty(value)
}
