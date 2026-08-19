package mcpserver

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffFileStoreWriteAtomicallyReplacesExistingFile(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := newDiffFileStore(defaultDiffCacheBytes)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(store.Close())
	})

	path, size, err := store.write("evidence.diff", []byte("old evidence\n"))
	require.NoError(err)
	assert.Equal(int64(len("old evidence\n")), size)
	require.NoError(os.Chmod(path, 0o400))

	replaced, size, err := store.write("evidence.diff", []byte("new evidence\n"))
	require.NoError(err)
	assert.Equal(path, replaced)
	assert.Equal(int64(len("new evidence\n")), size)

	data, err := os.ReadFile(path)
	require.NoError(err)
	assert.Equal("new evidence\n", string(data))
	info, err := os.Stat(path)
	require.NoError(err)
	assert.Equal(os.FileMode(0o600), info.Mode().Perm())
}

func TestDiffFileStoreFailedReplacementPreservesExistingEntries(t *testing.T) {
	store, err := newDiffFileStore(1024)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Chmod(store.dir, 0o700)
		require.NoError(t, store.Close())
	})

	existingPath, _, err := store.write("evidence.diff", []byte("old evidence\n"))
	require.NoError(t, err)
	otherPath, _, err := store.write("other.diff", []byte("other evidence\n"))
	require.NoError(t, err)
	sizeBefore := store.totalBytes

	require.NoError(t, os.Chmod(store.dir, 0o500))
	_, _, err = store.write("evidence.diff", []byte("replacement evidence\n"))
	require.Error(t, err)

	existing, err := os.ReadFile(existingPath)
	require.NoError(t, err)
	assert.Equal(t, "old evidence\n", string(existing))
	other, err := os.ReadFile(otherPath)
	require.NoError(t, err)
	assert.Equal(t, "other evidence\n", string(other))
	assert.Equal(t, sizeBefore, store.totalBytes)
	assert.Equal(t, 2, store.lru.Len())

	require.NoError(t, os.Chmod(store.dir, 0o700))
	replaced, _, err := store.write("evidence.diff", []byte("new evidence\n"))
	require.NoError(t, err)
	assert.Equal(t, existingPath, replaced)
}

func TestDiffFileStoreReplacementAccountsForReplacedEntrySize(t *testing.T) {
	store, err := newDiffFileStore(64)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	_, _, err = store.write("big.diff", make([]byte, 40))
	require.NoError(t, err)

	// Replacing the only entry with a larger payload must reuse the replaced
	// entry's budget instead of evicting it (or failing with no evictable
	// files) while it is still published.
	path, size, err := store.write("big.diff", make([]byte, 63))
	require.NoError(t, err)
	assert.Equal(t, int64(63), size)
	assert.Equal(t, int64(63), store.totalBytes)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Len(t, data, 63)
}

func TestDiffFileStoreReplacementEvictsOthersButNeverItself(t *testing.T) {
	store, err := newDiffFileStore(64)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	bigPath, _, err := store.write("big.diff", make([]byte, 40))
	require.NoError(t, err)
	smallPath, _, err := store.write("small.diff", make([]byte, 10))
	require.NoError(t, err)

	replaced, _, err := store.write("big.diff", make([]byte, 60))
	require.NoError(t, err)
	assert.Equal(t, bigPath, replaced)
	assert.Equal(t, int64(60), store.totalBytes)
	_, err = os.Stat(smallPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	data, err := os.ReadFile(replaced)
	require.NoError(t, err)
	assert.Len(t, data, 60)
}
