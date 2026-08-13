//go:build !windows

package testtmux

import (
	"os"
	"syscall"
)

func replaceWithTmux(path string, args []string) error {
	argv := append([]string{path}, args...)
	return syscall.Exec(path, argv, os.Environ())
}
