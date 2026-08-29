package runtimelock

import (
	"io/fs"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureNodeIDPersistsStableRandomIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()

	first, err := EnsureNodeID(dir)
	require.NoError(err)
	assert.Regexp(`^[0-9a-f]{32}$`, first)

	second, err := EnsureNodeID(dir)
	require.NoError(err)
	assert.Equal(first, second)

	info, err := os.Stat(NodeIDPath(dir))
	require.NoError(err)
	assert.Equal(fs.FileMode(0o600), info.Mode().Perm())
}

func TestEnsureNodeIDConcurrentCallersReuseWinner(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const callers = 32
	dir := t.TempDir()
	gate := make(chan struct{})
	identities := make(chan string, callers)
	errs := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)

	for range callers {
		go func() {
			ready.Done()
			<-gate
			identity, err := EnsureNodeID(dir)
			identities <- identity
			errs <- err
		}()
	}
	ready.Wait()
	close(gate)

	winner := ""
	for range callers {
		require.NoError(<-errs)
		identity := <-identities
		if winner == "" {
			winner = identity
		}
		assert.Equal(winner, identity)
	}
}

func TestEnsureNodeIDRejectsMalformedExistingIdentity(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	malformed := []byte("not-a-node-id\n")
	require.NoError(os.WriteFile(NodeIDPath(dir), malformed, 0o600))

	_, err := EnsureNodeID(dir)
	require.ErrorContains(err, "invalid node ID")

	got, err := os.ReadFile(NodeIDPath(dir))
	require.NoError(err)
	assert.Equal(t, malformed, got)
}
