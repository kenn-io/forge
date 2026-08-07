//go:build windows

package main

import (
	"fmt"
	"os"
)

func signalDaemonProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	return process.Kill()
}
