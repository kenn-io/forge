//go:build !windows

package ptysize

import (
	"fmt"
	"os"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Resize applies browser geometry, retaining existing per-cell pixel geometry
// when the caller has no measured pixels.
func Resize(ptmx *os.File, geometry Geometry) error {
	conn, err := ptmx.SyscallConn()
	if err != nil {
		return fmt.Errorf("access PTY for resize: %w", err)
	}
	var resizeErr error
	// Control keeps the descriptor valid during resize without switching to blocking I/O.
	if err := conn.Control(func(fd uintptr) {
		current, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
		if err != nil {
			resizeErr = fmt.Errorf("get PTY size before resize: %w", err)
			return
		}
		next := Apply(&pty.Winsize{
			Rows: current.Row, Cols: current.Col, X: current.Xpixel, Y: current.Ypixel,
		}, geometry)
		if err := unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{
			Row: next.Rows, Col: next.Cols, Xpixel: next.X, Ypixel: next.Y,
		}); err != nil {
			resizeErr = fmt.Errorf("set PTY size: %w", err)
		}
	}); err != nil {
		return fmt.Errorf("access PTY for resize: %w", err)
	}
	return resizeErr
}
