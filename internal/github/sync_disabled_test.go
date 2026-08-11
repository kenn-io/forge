package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestDisabledSyncerDoesNotStartAndGatesProviders(t *testing.T) {
	assert := assert.New(t)
	syncer := NewSyncer(nil, dbtest.Open(t), nil, nil, time.Millisecond, nil, nil)
	t.Cleanup(syncer.Stop)
	assert.True(syncer.SyncEnabled())

	syncer.DisableSync()
	syncer.Start(t.Context())
	syncer.RunOnce(t.Context())

	assert.False(syncer.SyncEnabled())
	assert.Empty(syncer.Status().LastRunAt)
	_, err := syncer.SyncRegistry().Provider("github", "github.com")
	require.ErrorIs(t, err, ErrSyncDisabled)
}
