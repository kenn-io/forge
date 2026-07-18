package workspace

import (
	"os"
	"path/filepath"
)

type openedWorktreePath struct {
	file          *os.File
	info          os.FileInfo
	symlinkTarget string
}

func openWorktreePath(root, relative string) (*openedWorktreePath, error) {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rooted.Close()

	path := filepath.FromSlash(relative)
	info, err := rooted.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := rooted.Readlink(path)
		if readErr != nil {
			return nil, readErr
		}
		return &openedWorktreePath{symlinkTarget: target}, nil
	}
	if !info.Mode().IsRegular() {
		return nil, errWorktreePathNotRegular
	}
	file, err := openWorktreeFile(rooted, path)
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, errWorktreePathNotRegular
	}
	return &openedWorktreePath{file: file, info: openedInfo}, nil
}
