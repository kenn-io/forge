package mcpserver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/atomicfile"
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
	assert := assert.New(t)
	require := require.New(t)
	store, err := newDiffFileStore(1024)
	require.NoError(err)
	t.Cleanup(func() {
		_ = os.Chmod(store.dir, 0o700)
		require.NoError(store.Close())
	})

	existingPath, _, err := store.write("evidence.diff", []byte("old evidence\n"))
	require.NoError(err)
	otherPath, _, err := store.write("other.diff", []byte("other evidence\n"))
	require.NoError(err)
	sizeBefore := store.totalBytes

	require.NoError(os.Chmod(store.dir, 0o500))
	_, _, err = store.write("evidence.diff", []byte("replacement evidence\n"))
	require.Error(err)

	existing, err := os.ReadFile(existingPath)
	require.NoError(err)
	assert.Equal("old evidence\n", string(existing))
	other, err := os.ReadFile(otherPath)
	require.NoError(err)
	assert.Equal("other evidence\n", string(other))
	assert.Equal(sizeBefore, store.totalBytes)
	assert.Equal(2, store.lru.Len())

	require.NoError(os.Chmod(store.dir, 0o700))
	replaced, _, err := store.write("evidence.diff", []byte("new evidence\n"))
	require.NoError(err)
	assert.Equal(existingPath, replaced)
}

func TestDiffFileStoreReplacementAccountsForReplacedEntrySize(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := newDiffFileStore(64)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(store.Close())
	})

	_, _, err = store.write("big.diff", make([]byte, 40))
	require.NoError(err)

	// Replacing the only entry with a larger payload must reuse the replaced
	// entry's budget instead of evicting it (or failing with no evictable
	// files) while it is still published.
	path, size, err := store.write("big.diff", make([]byte, 63))
	require.NoError(err)
	assert.Equal(int64(63), size)
	assert.Equal(int64(63), store.totalBytes)
	data, err := os.ReadFile(path)
	require.NoError(err)
	assert.Len(data, 63)
}

func TestDiffFileStoreReplacementEvictsOthersButNeverItself(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := newDiffFileStore(64)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(store.Close())
	})

	bigPath, _, err := store.write("big.diff", make([]byte, 40))
	require.NoError(err)
	smallPath, _, err := store.write("small.diff", make([]byte, 10))
	require.NoError(err)

	replaced, _, err := store.write("big.diff", make([]byte, 60))
	require.NoError(err)
	assert.Equal(bigPath, replaced)
	assert.Equal(int64(60), store.totalBytes)
	_, err = os.Stat(smallPath)
	require.ErrorIs(err, os.ErrNotExist)
	data, err := os.ReadFile(replaced)
	require.NoError(err)
	assert.Len(data, 60)
}

func TestDiffFileStoreFailedCommitKeepsEntriesItWouldEvict(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := newDiffFileStore(64)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(store.Close())
	})

	oldestPath, _, err := store.write("oldest.diff", make([]byte, 30))
	require.NoError(err)
	newerPath, _, err := store.write("newer.diff", make([]byte, 30))
	require.NoError(err)

	// A directory appearing at the target makes the real Commit refuse to
	// publish, after the cache would already need to evict oldest.diff.
	store.commit = func(f *atomicfile.File) error {
		require.NoError(os.Mkdir(f.Name(), 0o700))
		return f.Commit()
	}
	_, _, err = store.write("incoming.diff", make([]byte, 30))
	require.Error(err)

	for _, path := range []string{oldestPath, newerPath} {
		data, err := os.ReadFile(path)
		require.NoError(err, filepath.Base(path))
		assert.Len(data, 30)
	}
	assert.Equal(int64(60), store.totalBytes)
	assert.Equal(2, store.lru.Len())
	assert.NotContains(store.entries, "incoming.diff")
}

func TestDiffFileStoreFailedEvictionKeepsEntryCounted(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := newDiffFileStore(64)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(store.Close())
	})

	oldestPath, _, err := store.write("oldest.diff", make([]byte, 30))
	require.NoError(err)
	_, _, err = store.write("newer.diff", make([]byte, 30))
	require.NoError(err)

	// Removal can fail, for example on Windows while a reader holds the
	// evicted diff open.
	store.remove = func(string) error { return errors.New("file in use") }
	incomingPath, _, err := store.write("incoming.diff", make([]byte, 30))
	require.NoError(err, "the new diff already landed")
	_, err = os.Stat(incomingPath)
	require.NoError(err)
	_, err = os.Stat(oldestPath)
	require.NoError(err)
	assert.Contains(store.entries, "oldest.diff")
	assert.Equal(int64(90), store.totalBytes)
	assert.Equal(3, store.lru.Len())

	// A later write retries the eviction once removal works again.
	store.remove = os.Remove
	_, _, err = store.write("latest.diff", make([]byte, 4))
	require.NoError(err)
	_, err = os.Stat(oldestPath)
	require.ErrorIs(err, os.ErrNotExist)
	assert.NotContains(store.entries, "oldest.diff")
	assert.LessOrEqual(store.totalBytes, store.maxBytes)
}
