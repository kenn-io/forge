package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

type syncDisabledProvider struct{}

func (syncDisabledProvider) Platform() platform.Kind { return platform.KindGitHub }
func (syncDisabledProvider) Host() string            { return platform.DefaultGitHubHost }
func (syncDisabledProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{}
}

func TestDisabledSyncerDoesNotStartAndGatesProviders(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	registry, err := platform.NewRegistry(syncDisabledProvider{})
	require.NoError(err)
	syncer := NewSyncerWithRegistry(
		registry, dbtest.Open(t), nil, nil, time.Millisecond, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	assert.True(syncer.SyncEnabled())

	syncer.DisableSync()
	syncer.Start(t.Context())
	syncer.RunOnce(t.Context())

	assert.False(syncer.SyncEnabled())
	assert.Empty(syncer.Status().LastRunAt)
	_, err = syncer.clients.Provider("github", "github.com")
	require.ErrorIs(err, ErrSyncDisabled)
	_, err = syncer.Registry().Provider("github", "github.com")
	require.ErrorIs(err, ErrSyncDisabled)
	_, err = syncer.DirectRegistry().Provider("github", "github.com")
	require.NoError(err)
}
