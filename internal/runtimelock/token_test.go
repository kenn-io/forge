package runtimelock

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureAuthToken pins the token contract thin clients rely on:
// minted once with user-only permissions, stable across restarts.
func TestEnsureAuthToken(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()

	token, err := EnsureAuthToken(dir)
	require.NoError(err)
	assert.Len(token, 64, "32 random bytes hex-encoded")

	info, err := os.Stat(AuthTokenPath(dir))
	require.NoError(err)
	assert.Equal(os.FileMode(0o600), info.Mode().Perm(),
		"file mode is the authorization boundary")

	again, err := EnsureAuthToken(dir)
	require.NoError(err)
	assert.Equal(token, again,
		"restarts must not invalidate connected clients")

	read, err := ReadAuthToken(dir)
	require.NoError(err)
	assert.Equal(token, read)
}

// TestReadAuthTokenAbsent reports empty (not an error) when no token
// was ever minted, so probing clients can distinguish "no auth" from
// failure.
func TestReadAuthTokenAbsent(t *testing.T) {
	token, err := ReadAuthToken(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, token)
}

// TestEnsureAuthTokenRestrictsExistingMode pins the boundary repair:
// a token file that pre-exists with loose permissions is chmodded
// back to 0600 on reuse.
func TestEnsureAuthTokenRestrictsExistingMode(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	require.NoError(os.WriteFile(
		AuthTokenPath(dir), []byte("tok\n"), 0o644,
	))

	token, err := EnsureAuthToken(dir)
	require.NoError(err)
	require.Equal("tok", token)
	info, err := os.Stat(AuthTokenPath(dir))
	require.NoError(err)
	require.Equal(os.FileMode(0o600), info.Mode().Perm())
}

// TestEnsureAuthTokenReplacesEmptyFile pins that an empty token file
// is replaced by a fresh mint with restricted mode.
func TestEnsureAuthTokenReplacesEmptyFile(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	require.NoError(os.WriteFile(AuthTokenPath(dir), nil, 0o644))

	token, err := EnsureAuthToken(dir)
	require.NoError(err)
	require.Len(token, 64)
	info, err := os.Stat(AuthTokenPath(dir))
	require.NoError(err)
	require.Equal(os.FileMode(0o600), info.Mode().Perm())
}
