package ptyowner

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client polling HasState can hold owner.json open at the moment the owner
// removes its state directory. On Windows, a handle opened without
// FILE_SHARE_DELETE makes that removal fail, leaving the state behind forever.
func TestOwnerStateRemovalSucceedsWhileReaderHoldsStateFile(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	paths, err := NewSessionPaths(root, "held-state")
	require.NoError(err)
	require.NoError(writeState(paths, ownerState{
		Session: "held-state", Addr: "unix://" + paths.Socket, Token: "token",
		CreatedAt: time.Now().UTC(),
	}))
	client := &Client{Root: root}
	require.True(client.HasState("held-state"))
	reader, err := openStateFile(paths.StatePath)
	require.NoError(err)
	t.Cleanup(func() { _ = reader.Close() })

	require.NoError(removeOwnerState(paths))

	assert.NoDirExists(paths.Dir)
	assert.False(client.HasState("held-state"))
}
