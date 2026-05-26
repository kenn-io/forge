//go:build unix

package ptyowner

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func createPrivateSocketDir(path string) error {
	if err := os.Mkdir(path, 0o700); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}

	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing fallback socket dir symlink %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("fallback socket path is not a directory: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot inspect fallback socket dir ownership: %s", path)
	}
	currentUID := uint32(os.Geteuid())
	if stat.Uid != currentUID {
		return fmt.Errorf(
			"fallback socket dir %s is owned by uid %d, not current uid %d",
			path, stat.Uid, currentUID,
		)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf(
			"fallback socket dir %s has permissions %03o; expected private directory",
			path, info.Mode().Perm(),
		)
	}
	return nil
}
