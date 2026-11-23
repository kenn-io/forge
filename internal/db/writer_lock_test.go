package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A replicator such as Litestream opens its own connection and takes the
// write lock for each WAL checkpoint. Here it tries to do so between the
// daemon's read and its write. The daemon's transaction must already hold the
// write lock, so the replicator waits its turn and the daemon's write lands.
// Under a deferred BEGIN the replicator got the lock instead, and the
// daemon's write failed with SQLITE_BUSY without waiting.
func TestWriteTransactionHoldsLockAgainstReplicator(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "forge.db")
	d, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })

	replicator, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)")
	require.NoError(t, err)
	t.Cleanup(func() { replicator.Close() })
	conn, err := replicator.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	var replicatorErr error
	replicatorLocked := false
	value, err := d.GetOrCreateAppMetadataValue(ctx, "instance-id", func() (string, error) {
		_, replicatorErr = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		replicatorLocked = replicatorErr == nil
		return "created", nil
	})
	if replicatorLocked {
		_, rollbackErr := conn.ExecContext(ctx, "ROLLBACK")
		require.NoError(t, rollbackErr)
	}

	require.NoError(t, err, "the daemon's write must not lose the lock to the replicator")
	assert.Equal(t, "created", value)
	require.Error(t, replicatorErr, "the replicator must wait while the daemon's transaction is open")

	stored, found, err := d.AppMetadataValue(ctx, "instance-id")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "created", stored)
}
