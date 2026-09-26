package devbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

// serveUnix keeps recovery and shutdown under one ownership lock. The lock file
// stays on disk so concurrent starts always lock the same inode.
func serveUnix(ctx context.Context, path string, mode os.FileMode, server *http.Server) (err error) {
	lock := flock.New(path + ".lock")
	defer func() { err = errors.Join(err, lock.Close()) }()
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("unix socket already owned: %s", path)
	}
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket path: %s", path)
		}
		conn, dialErr := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
		if dialErr == nil {
			_ = conn.Close()
			return fmt.Errorf("unix socket already listening: %s", path)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return fmt.Errorf("check existing Unix socket: %w", dialErr)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer listener.Close() // UnixListener unlinks its socket on close.
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return serveLocal(ctx, listener, server)
}
