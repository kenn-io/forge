//go:build windows

package testtmux

import (
	"os"

	"go.kenn.io/forge/internal/procutil"
)

func replaceWithTmux(path string, args []string) error {
	command := procutil.Command(path, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
