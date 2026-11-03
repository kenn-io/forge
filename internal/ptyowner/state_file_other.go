//go:build !windows

package ptyowner

import "os"

func openStateFile(path string) (*os.File, error) {
	return os.Open(path)
}
