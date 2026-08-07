//go:build darwin

package pathidentity

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const darwinPathMax = 1024

// CanonicalExisting returns the filesystem-reported path for an existing file.
func CanonicalExisting(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buffer := make([]byte, darwinPathMax)
	_, err = unix.FcntlInt(
		file.Fd(),
		unix.F_GETPATH,
		int(uintptr(unsafe.Pointer(&buffer[0]))),
	)
	runtime.KeepAlive(buffer)
	if err != nil {
		return "", fmt.Errorf("get path from file handle: %w", err)
	}
	pathBytes, _, found := bytes.Cut(buffer, []byte{0})
	if !found {
		return "", fmt.Errorf("get path from file handle: unterminated path")
	}
	return string(pathBytes), nil
}
