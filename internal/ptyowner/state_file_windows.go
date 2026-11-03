//go:build windows

package ptyowner

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// openStateFile shares delete access so a client reading owner.json never
// blocks the owner from removing its state directory: os.Open omits
// FILE_SHARE_DELETE, and os.RemoveAll does not retry a sharing violation.
func openStateFile(path string) (*os.File, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(
		path16,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
