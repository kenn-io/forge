package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/runtimelock"
)

func TestDefaultBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		port       int
		basePath   string
		want       string
	}{
		{"loopback with base", "127.0.0.1", 8090, "/middleman/", "http://127.0.0.1:8090/middleman"},
		{"unspecified v4 to loopback", "0.0.0.0", 8090, "/middleman/", "http://127.0.0.1:8090/middleman"},
		{"unspecified v6 to loopback", "::", 8090, "/middleman/", "http://127.0.0.1:8090/middleman"},
		{"ipv6 literal bracketed", "::1", 8091, "/", "http://[::1]:8091"},
		{"root base path", "127.0.0.1", 8090, "/", "http://127.0.0.1:8090"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := &runtimelock.Metadata{Host: tc.host, Port: tc.port, BasePath: tc.basePath}
			assert.Equal(t, tc.want, defaultBaseURL(meta))
		})
	}
}

// TestAuthRotateOutputHeldLock pins the daemon-running behavior against
// a genuinely held runtime lock (not a pre-computed boolean, which
// would race a daemon starting between the check and the rotation):
// rotation refuses without --force and leaves the token untouched, and
// force-rotates with the restart warning.
func TestAuthRotateOutputHeldLock(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	first, err := runtimelock.EnsureAuthToken(dir)
	require.NoError(err)
	handle, err := runtimelock.Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = handle.Release() })

	var out, errOut bytes.Buffer
	err = authRotateOutput(dir, false, &out, &errOut)
	require.Error(err)
	assert.Contains(err.Error(), "--force")
	unchanged, err := runtimelock.ReadAuthToken(dir)
	require.NoError(err)
	assert.Equal(first, unchanged,
		"a refused rotation must not touch the token file")

	require.NoError(authRotateOutput(dir, true, &out, &errOut))
	rotated, err := runtimelock.ReadAuthToken(dir)
	require.NoError(err)
	assert.NotEqual(first, rotated)
	assert.Contains(errOut.String(), "OLD token")
}

func TestAuthTokenOutput(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	token, err := runtimelock.EnsureAuthToken(dir)
	require.NoError(err)

	var out bytes.Buffer
	require.NoError(authTokenOutput(dir, &out))
	require.Equal(token, strings.TrimSpace(out.String()))

	require.Error(authTokenOutput(t.TempDir(), &out), "absent token is an error")
}

func TestAuthURLOutputWithBaseURL(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	token, err := runtimelock.EnsureAuthToken(dir)
	require.NoError(err)

	var out bytes.Buffer
	require.NoError(authURLOutput(dir, "https://middleman.example.com/middleman/", &out))
	got := strings.TrimSpace(out.String())
	require.True(strings.HasPrefix(got, "https://middleman.example.com/middleman/?auth_token="), got)
	require.Contains(got, token)
}

func TestAuthURLOutputNoDaemonNoBaseURL(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	_, err := runtimelock.EnsureAuthToken(dir)
	require.NoError(err)

	var out bytes.Buffer
	err = authURLOutput(dir, "", &out)
	require.Error(err)
	require.Contains(err.Error(), "--base-url")
}

func TestAuthRotateOutputOffline(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	first, err := runtimelock.EnsureAuthToken(dir)
	require.NoError(err)

	var out, errOut bytes.Buffer
	require.NoError(authRotateOutput(dir, false, &out, &errOut))
	require.NotEqual(first, strings.TrimSpace(out.String()))
	require.Contains(errOut.String(), "Restart")
}

func TestAuthURLOutputAbsentToken(t *testing.T) {
	require := require.New(t)
	var out bytes.Buffer
	err := authURLOutput(t.TempDir(), "https://host/", &out)
	require.Error(err)
	require.Contains(err.Error(), "start the daemon")
}
