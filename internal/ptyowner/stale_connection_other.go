//go:build !windows

package ptyowner

func isPlatformStaleOwnerConnection(error) bool {
	return false
}
