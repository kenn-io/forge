//go:build unix

package devbox

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryRecoversStaleSocketAndKeepsActiveListener(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	path := filepath.Join(t.TempDir(), "registry.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(err)
	// Leave the same unbound filesystem socket an abrupt process exit leaves.
	stale.SetUnlinkOnClose(false)
	require.NoError(stale.Close())
	cfg := RegistryConfig{Socket: path, RegistryID: "example", Revision: "1"}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan struct{})
	var serveErr error
	go func() { defer close(finished); serveErr = ServeRegistry(ctx, cfg) }()
	t.Cleanup(func() { cancel(); <-finished })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: time.Second}
	healthy := func() bool {
		response, err := client.Get("http://registry/healthz")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	}
	require.Eventually(func() bool {
		select {
		case <-finished:
			return true
		default:
			return healthy()
		}
	}, 5*time.Second, 10*time.Millisecond)
	select {
	case <-finished:
		require.NoError(serveErr)
	default:
	}
	require.True(healthy())
	require.Error(ServeRegistry(t.Context(), cfg), "a second server must not unlink the live listener")
	assert.True(healthy())
	cancel()
	<-finished
	require.NoError(serveErr)
	_, err = os.Lstat(path)
	assert.ErrorIs(err, os.ErrNotExist, "normal shutdown removes its socket")
}

func TestRegistryDoesNotReplaceExistingFilesOrListeners(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "live socket"} {
		t.Run(kind, func(t *testing.T) {
			require := require.New(t)
			path := filepath.Join(t.TempDir(), "registry.sock")
			switch kind {
			case "file":
				require.NoError(os.WriteFile(path, []byte("keep"), 0o600))
			case "symlink":
				require.NoError(os.Symlink("missing", path))
			case "live socket":
				listener, err := net.Listen("unix", path)
				require.NoError(err)
				t.Cleanup(func() { _ = listener.Close() })
			}
			before, err := os.Lstat(path)
			require.NoError(err)
			require.Error(ServeRegistry(t.Context(), RegistryConfig{Socket: path, RegistryID: "example", Revision: "1"}))
			after, err := os.Lstat(path)
			require.NoError(err)
			require.True(os.SameFile(before, after))
		})
	}
}
