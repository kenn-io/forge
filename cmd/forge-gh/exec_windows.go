package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"go.kenn.io/forge/internal/procutil"
)

const ghExecutable = "gh.exe"

func passthrough(path string, args []string) int {
	cmd := procutil.Command(path, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
