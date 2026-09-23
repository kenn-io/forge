//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

const ghExecutable = "gh"

func passthrough(path string, args []string) int {
	if err := syscall.Exec(path, append([]string{"gh"}, args...), os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return 1
}
