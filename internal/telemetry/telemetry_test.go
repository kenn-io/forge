package telemetry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/posthog/posthog-go"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePostHogClient struct {
	message posthog.Message
}

func (f *fakePostHogClient) Enqueue(message posthog.Message) error {
	f.message = message
	return nil
}

func (f *fakePostHogClient) Close() error { return nil }

func TestNewReporterDisabledByEnvDoesNotCreateInstallID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	t.Setenv(EnabledEnv, "0")
	dataDir := t.TempDir()

	reporter, err := NewReporter(Options{DataDir: dataDir})
	require.NoError(err)

	assert.False(reporter.Enabled())
	assert.NoFileExists(filepath.Join(dataDir, installIDFile))
}

func TestLoadOrCreateInstallIDIsStableAndAnonymous(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dataDir := t.TempDir()

	first, err := loadOrCreateInstallID(dataDir)
	require.NoError(err)
	second, err := loadOrCreateInstallID(dataDir)
	require.NoError(err)

	assert.Len(first, 32)
	assert.Equal(first, second)

	info, err := os.Stat(filepath.Join(dataDir, installIDFile))
	require.NoError(err)
	assert.Equal(os.FileMode(0o600), info.Mode().Perm())
}

func TestReporterCaptureUsesAnonymousDistinctID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	client := &fakePostHogClient{}
	reporter := &Reporter{
		client:     client,
		distinctID: "anonymous-install-id",
		enabled:    true,
	}

	err := reporter.Capture("app_loaded", map[string]any{
		"distinct_id": "user-provided",
		"view":        "pulls",
	})
	require.NoError(err)

	capture, ok := client.message.(posthog.Capture)
	require.True(ok)
	assert.Equal("anonymous-install-id", capture.DistinctId)
	assert.Equal("app_loaded", capture.Event)
	assert.Equal("pulls", capture.Properties["view"])
	assert.NotContains(capture.Properties, "distinct_id")
	assert.True(capture.Properties["$geoip_disable"].(bool))
}
