package runtimelock

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthNonceSingleUse pins the bootstrap-nonce contract: a minted
// nonce is consumed exactly once, and unknown or empty nonces are
// rejected.
func TestAuthNonceSingleUse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()

	nonce, err := MintAuthNonce(dir)
	require.NoError(err)
	assert.Len(nonce, 64, "32 random bytes hex-encoded")

	assert.True(ConsumeAuthNonce(dir, nonce), "first consume succeeds")
	assert.False(ConsumeAuthNonce(dir, nonce), "a nonce is single-use")
	assert.False(ConsumeAuthNonce(dir, "unknown"))
	assert.False(ConsumeAuthNonce(dir, ""))
}

// TestAuthNonceExpiry pins the TTL: an expired nonce is rejected on
// consume, and minting sweeps expired leftovers from the store.
func TestAuthNonceExpiry(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()

	expired, err := MintAuthNonce(dir)
	require.NoError(err)
	stale := time.Now().Add(-AuthNonceTTL - time.Minute)
	require.NoError(os.Chtimes(authNoncePath(dir, expired), stale, stale))

	assert.False(ConsumeAuthNonce(dir, expired),
		"an expired nonce must not authorize a login")

	leftover, err := MintAuthNonce(dir)
	require.NoError(err)
	require.NoError(os.Chtimes(authNoncePath(dir, leftover), stale, stale))
	_, err = MintAuthNonce(dir)
	require.NoError(err)
	assert.NoFileExists(authNoncePath(dir, leftover),
		"minting sweeps expired leftovers")
}

// TestAuthNonceFilesAreHashedAndRestricted pins that the on-disk store
// never contains a usable credential: filenames are digests of the
// nonce, not the nonce itself, and entries are user-only.
func TestAuthNonceFilesAreHashedAndRestricted(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()

	nonce, err := MintAuthNonce(dir)
	require.NoError(err)

	entries, err := os.ReadDir(authNonceDir(dir))
	require.NoError(err)
	require.Len(entries, 1)
	assert.NotEqual(nonce, entries[0].Name())
	info, err := entries[0].Info()
	require.NoError(err)
	assert.Equal(os.FileMode(0o600), info.Mode().Perm())
}
