package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestDisabledSyncerDoesNotStartOrRun(t *testing.T) {
	syncer := NewSyncer(nil, dbtest.Open(t), nil, nil, time.Millisecond, nil, nil)
	t.Cleanup(syncer.Stop)
	assert.True(t, syncer.SyncEnabled())

	syncer.DisableSync()
	syncer.Start(t.Context())
	syncer.TriggerRun(t.Context())
	syncer.RunOnce(t.Context())

	assert.False(t, syncer.SyncEnabled())
	assert.Empty(t, syncer.Status().LastRunAt)
}

func TestDisabledSyncerRejectsProviderRefreshes(t *testing.T) {
	syncer := NewSyncer(nil, dbtest.Open(t), nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	syncer.DisableSync()

	require.ErrorIs(t, syncer.SyncRepoOnProvider(
		t.Context(), platform.KindGitHub, "github.com", "acme", "widget",
	), ErrSyncDisabled)
	require.ErrorIs(t, syncer.RunNotificationSync(t.Context()), ErrSyncDisabled)
	require.ErrorIs(
		t,
		syncer.ProcessQueuedNotificationReadsForAllHosts(t.Context(), 25),
		ErrSyncDisabled,
	)
}

func TestDisabledSyncerUpdatesReposWithoutArchiveSeeding(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	syncer := NewSyncer(nil, dbtest.Open(t), nil, nil, time.Minute, nil, nil)
	recorder := &archiveLifecycleRecorder{}
	syncer.SetArchiveService(recorder)
	syncer.DisableSync()
	repos := []RepoRef{{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}}

	require.NoError(syncer.SetReposWithContext(t.Context(), repos, true))
	assert.Empty(recorder.ensured)
	assert.Empty(recorder.retried)
	assert.Equal(repos, syncer.TrackedRepos())
}
