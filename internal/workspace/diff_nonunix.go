//go:build !unix

package workspace

import (
	"errors"
	"os"
)

func openRegularUntrackedFile(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, errors.New("untracked path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err = file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, errors.New("untracked path is not a regular file")
	}
	return file, info, nil
}

func openWorktreeFile(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}
