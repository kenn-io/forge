//go:build windows

package ptyowner

import (
	"context"
	"os"
	"strconv"
	"unicode/utf16"

	gopty "github.com/aymanbagabas/go-pty"
	"go.kenn.io/forge/internal/procutil"
	"golang.org/x/sys/windows"
)

func validateCommandLine(command []string) error {
	// CreateProcessW allows 32,767 UTF-16 code units, including the terminating NUL.
	if len(utf16.Encode([]rune(windows.ComposeCommandLine(command)))) >= 32767 {
		return ErrCommandLineTooLong
	}
	return nil
}

func configureOwnerCommand(*gopty.Cmd) {}

func killOwnerProcess(process *os.Process) {
	cmd := procutil.Command(
		"taskkill", "/T", "/F", "/PID", strconv.Itoa(process.Pid),
	)
	err := procutil.Run(context.Background(), cmd, "taskkill subprocess capacity")
	if err != nil {
		_ = process.Kill()
	}
}
