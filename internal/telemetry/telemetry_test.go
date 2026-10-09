package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/kit/telemetry/posthog"
)

type capturedEvent struct {
	event      string
	properties map[string]any
}

type fakeKitClient struct {
	captures []capturedEvent
	closed   bool
}

func (f *fakeKitClient) Capture(event string, properties map[string]any) error {
	f.captures = append(f.captures, capturedEvent{event: event, properties: properties})
	return nil
}

func (f *fakeKitClient) Report(_ context.Context, event string, properties map[string]any) (posthog.Status, error) {
	return posthog.StatusQueued, f.Capture(event, properties)
}

func (f *fakeKitClient) Close() error {
	f.closed = true
	return nil
}

func (f *fakeKitClient) Enabled() bool {
	return true
}

func TestNewReporterDisabledInGoTestEvenWhenEnvEnabled(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	t.Setenv(EnabledEnv, "1")
	t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
	database := dbtest.Open(t)

	reporter, err := NewReporter(Options{Database: database})
	require.NoError(err)

	assert.False(reporter.Enabled())
	_, found, err := database.AppMetadataValue(t.Context(), InstallIDMetadataKey)
	require.NoError(err)
	assert.False(found)
}

func TestLoadOrCreateInstallIDIsStableAndAnonymous(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)

	first, _, err := loadOrCreateInstallID(t.Context(), database, time.Now())
	require.NoError(err)
	second, _, err := loadOrCreateInstallID(t.Context(), database, time.Now())
	require.NoError(err)

	assert.Len(first, 32)
	assert.Equal(first, second)

	stored, found, err := database.AppMetadataValue(t.Context(), InstallIDMetadataKey)
	require.NoError(err)
	assert.True(found)
	assert.Equal(first, stored)
}

func TestReporterRoutesEventsBySource(t *testing.T) {
	tests := []struct {
		name        string
		event       string
		wantErr     error
		wantDaemon  int
		wantBackend int
	}{
		{name: "daemon_active goes to daemon", event: "daemon_active", wantDaemon: 1},
		{name: "app_opened goes to backend", event: " app_opened ", wantBackend: 1},
		{name: "unsupported event", event: "server_started", wantErr: ErrUnsupportedEvent},
		{name: "empty event", event: " "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)

			daemon := &fakeKitClient{}
			backend := &fakeKitClient{}
			reporter := &Reporter{daemon: daemon, backend: backend}

			err := reporter.Capture(tt.event, map[string]any{"repo_count": 1})
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantDaemon+tt.wantBackend == 0:
				require.Error(t, err)
			default:
				require.NoError(t, err)
			}

			assert.Len(daemon.captures, tt.wantDaemon)
			assert.Len(backend.captures, tt.wantBackend)
		})
	}
}

func TestReporterCloseClosesBothClients(t *testing.T) {
	daemon := &fakeKitClient{}
	backend := &fakeKitClient{}
	reporter := &Reporter{daemon: daemon, backend: backend}

	require.NoError(t, reporter.Close())

	assert.True(t, daemon.closed)
	assert.True(t, backend.closed)
}

func TestDisabledReporterIsNoOp(t *testing.T) {
	assert := assert.New(t)

	for _, reporter := range []*Reporter{nil, DisabledReporter()} {
		assert.False(reporter.Enabled())
		assert.NoError(reporter.Capture("server_started", nil))
		assert.NoError(reporter.Close())
	}
}

func TestUIEventAllowedAdmitsOnlyBackendEvents(t *testing.T) {
	assert := assert.New(t)

	assert.True(UIEventAllowed(" app_opened "))
	assert.True(UIEventAllowed("session_ended"))
	assert.False(UIEventAllowed("daemon_active"))
	assert.False(UIEventAllowed("app_loaded"))
}

func TestLoadOrCreateInstallIDRecordsCreationTimeOnce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)

	_, first, err := loadOrCreateInstallID(t.Context(), database, created)
	require.NoError(err)
	_, second, err := loadOrCreateInstallID(t.Context(), database, created.Add(48*time.Hour))
	require.NoError(err)

	assert.True(created.Equal(first))
	assert.True(created.Equal(second))
}

func TestLoadOrCreateInstallIDLeavesPreexistingIDWithoutCreationTime(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	_, err := database.GetOrCreateAppMetadataValue(t.Context(), InstallIDMetadataKey, func() (string, error) {
		return "existing-install-id", nil
	})
	require.NoError(err)

	id, installedAt, err := loadOrCreateInstallID(t.Context(), database, time.Now())
	require.NoError(err)

	assert.Equal("existing-install-id", id)
	assert.True(installedAt.IsZero())
	_, found, err := database.AppMetadataValue(t.Context(), installedAtKey)
	require.NoError(err)
	assert.False(found)
}

func TestLoadOrCreateInstallIDTreatsUnparseableTimeAsUnknown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	_, err := database.GetOrCreateAppMetadataValue(t.Context(), installedAtKey, func() (string, error) {
		return "not-a-time", nil
	})
	require.NoError(err)

	id, installedAt, err := loadOrCreateInstallID(t.Context(), database, time.Now())
	require.NoError(err)

	assert.Len(id, 32)
	assert.True(installedAt.IsZero())
}
