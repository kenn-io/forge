//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
)

func signalDaemonProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	killErr := process.Kill()
	releaseErr := process.Release()
	return errors.Join(killErr, releaseErr)
}
