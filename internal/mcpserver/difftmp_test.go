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
	store, err := newDiffFileStore()
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
