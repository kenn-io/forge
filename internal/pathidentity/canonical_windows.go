//go:build windows

package pathidentity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// CanonicalExisting returns the filesystem-reported path for an existing file.
func CanonicalExisting(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buffer := make([]uint16, windows.MAX_PATH)
	for {
		length, err := windows.GetFinalPathNameByHandle(
			windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0,
		)
		if err != nil {
			return "", fmt.Errorf("get path from file handle: %w", err)
		}
		if length >= uint32(len(buffer)) {
			buffer = make([]uint16, length+1)
			continue
		}
		resolved := windows.UTF16ToString(buffer[:length])
		if strings.HasPrefix(resolved, `\\?\UNC\`) {
			resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
		} else {
			resolved = strings.TrimPrefix(resolved, `\\?\`)
		}
		return filepath.Clean(resolved), nil
	}
}
