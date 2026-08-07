//go:build linux

package pathidentity

import (
	"fmt"
	"os"
)

// CanonicalExisting returns the filesystem-reported path for an existing file.
func CanonicalExisting(path string) (string, error) {
	return canonicalExistingFromProc(path, "/proc/self/fd")
}

func canonicalExistingFromProc(path, procFDDir string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	resolved, err := os.Readlink(fmt.Sprintf("%s/%d", procFDDir, file.Fd()))
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			return path, nil
		}
		return "", fmt.Errorf("get path from file handle: %w", err)
	}
	return resolved, nil
}
