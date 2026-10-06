//go:build windows

package ptyowner

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// WSAENETDOWN is what dialing an AF_UNIX socket returns once its directory is gone.
func isPlatformStaleOwnerConnection(err error) bool {
	if errors.Is(err, windows.WSAECONNREFUSED) ||
		errors.Is(err, windows.WSAECONNRESET) ||
		errors.Is(err, windows.WSAECONNABORTED) ||
		errors.Is(err, windows.WSAENETDOWN) {
		return true
	}
	errno, ok := errors.AsType[syscall.Errno](err)
	if !ok {
		return false
	}
	return errno == windows.WSAECONNREFUSED ||
		errno == windows.WSAECONNRESET ||
		errno == windows.WSAECONNABORTED ||
		errno == windows.WSAENETDOWN
}
