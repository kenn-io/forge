//go:build !unix

package ptyowner

func createPrivateSocketDir(path string) error {
	return createPrivateDir(path)
}
