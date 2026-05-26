package ptyowner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionPathsRejectUnsafeNames(t *testing.T) {
	tests := []string{"", "../ws", "a/b", `a\b`, "a\x00b"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := NewSessionPaths(t.TempDir(), name)
			require.Error(t, err)
		})
	}
}

func TestSessionPathsAreStable(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	root := t.TempDir()

	paths, err := NewSessionPaths(root, "middleman-abc123")

	require.NoError(err)
	assert.Equal(root, paths.Root)
	assert.Contains(paths.Dir, "middleman-abc123")
	assert.NotEmpty(paths.Socket)
	assert.Contains(paths.StatePath, "owner.json")
}

func TestSessionPathsHashFilesystemHostileNames(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	root := t.TempDir()

	paths, err := NewSessionPaths(root, "ws-1:codex")

	require.NoError(err)
	assert.Equal("ws-1:codex", paths.Session)
	assert.NotContains(filepath.Base(paths.Dir), ":")
	assert.Contains(filepath.Base(paths.Dir), "session-")
	assert.Equal(filepath.Join(paths.Dir, "owner.json"), paths.StatePath)
}

func TestSessionPathsUsePrivateSocketDirForLongRoots(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	root := filepath.Join(t.TempDir(), strings.Repeat("x", maxUnixSocketPathLen))

	paths, err := NewSessionPaths(root, "middleman-abc123")

	require.NoError(err)
	require.NotEmpty(paths.SocketDir)
	assert.Equal(filepath.Join(paths.SocketDir, "sock"), paths.Socket)
	assert.Equal(fallbackSocketDir(root, "middleman-abc123", os.TempDir()), paths.SocketDir)
	assert.LessOrEqual(len(paths.Socket), maxUnixSocketPathLen)
}

func TestSessionPathsUseShortPrivateTmpWhenTempDirIsTooLong(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("/private/tmp is a macOS-specific socket fallback")
	}
	assert := Assert.New(t)
	require := require.New(t)
	root := filepath.Join(t.TempDir(), strings.Repeat("x", maxUnixSocketPathLen))
	longTempDir := filepath.Join(t.TempDir(), strings.Repeat("long-temp-root-", 8))
	t.Setenv("TMPDIR", longTempDir)

	paths, err := NewSessionPaths(root, "middleman-abc123")

	require.NoError(err)
	expectedDir := filepath.Join(
		"/private/tmp",
		"middleman-pty-"+sessionSocketHash(root+"-middleman-abc123"),
	)
	assert.Equal(expectedDir, paths.SocketDir)
	assert.Equal(filepath.Join(expectedDir, "sock"), paths.Socket)
	assert.LessOrEqual(len(paths.Socket), maxUnixSocketPathLen)
}

func TestFallbackSocketDirSkipsPrivateTmpOffDarwin(t *testing.T) {
	assert := Assert.New(t)
	root := filepath.Join(t.TempDir(), strings.Repeat("x", maxUnixSocketPathLen))
	longTempDir := filepath.Join(t.TempDir(), strings.Repeat("long-temp-root-", 8))

	socketDir := fallbackSocketDirForOS(root, "middleman-abc123", longTempDir, "linux")

	expectedDir := filepath.Join(
		"/tmp",
		"middleman-pty-"+sessionSocketHash(root+"-middleman-abc123"),
	)
	assert.Equal(expectedDir, socketDir)
	assert.LessOrEqual(len(filepath.Join(socketDir, "sock")), maxUnixSocketPathLen)
}

func TestFallbackSocketDirUsesPrivateTmpOnDarwin(t *testing.T) {
	assert := Assert.New(t)
	root := filepath.Join(t.TempDir(), strings.Repeat("x", maxUnixSocketPathLen))
	longTempDir := filepath.Join(t.TempDir(), strings.Repeat("long-temp-root-", 8))

	socketDir := fallbackSocketDirForOS(root, "middleman-abc123", longTempDir, "darwin")

	expectedDir := filepath.Join(
		"/private/tmp",
		"middleman-pty-"+sessionSocketHash(root+"-middleman-abc123"),
	)
	assert.Equal(expectedDir, socketDir)
	assert.LessOrEqual(len(filepath.Join(socketDir, "sock")), maxUnixSocketPathLen)
}

func TestCreatePrivateSocketDirRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fallback socket hardening is Unix-specific")
	}
	require := require.New(t)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	socketDir := filepath.Join(parent, "middleman-pty-symlink")
	require.NoError(os.Mkdir(target, 0o700))
	require.NoError(os.Symlink(target, socketDir))

	err := createPrivateSocketDir(socketDir)

	require.Error(err)
	require.ErrorContains(err, "refusing fallback socket dir symlink")
}

func TestCreatePrivateSocketDirRejectsSharedExistingDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode fallback socket hardening is Unix-specific")
	}
	require := require.New(t)
	socketDir := filepath.Join(t.TempDir(), "middleman-pty-shared")
	require.NoError(os.Mkdir(socketDir, 0o755))

	err := createPrivateSocketDir(socketDir)

	require.Error(err)
	require.ErrorContains(err, "expected private directory")
}

func TestProtocolRequestRoundTrip(t *testing.T) {
	assert := Assert.New(t)
	want := Request{
		Type:  RequestAttach,
		Token: "secret",
		Cols:  132,
		Rows:  43,
		Data:  []byte{0, 1, 2, 'x'},
	}

	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(want))

	var got Request
	require.NoError(t, json.NewDecoder(&buf).Decode(&got))
	assert.Equal(want, got)
}

func TestProtocolResponseRoundTrip(t *testing.T) {
	assert := Assert.New(t)
	code := 7
	want := Response{
		Type:     ResponseExit,
		OK:       true,
		ExitCode: &code,
		Output:   []byte("recent output"),
		Title:    "workspace title",
	}

	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(want))

	var got Response
	require.NoError(t, json.NewDecoder(&buf).Decode(&got))
	assert.Equal(want, got)
}
