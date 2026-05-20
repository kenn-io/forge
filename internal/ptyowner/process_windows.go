//go:build windows

package ptyowner

import (
	"context"
	"os"
	"strconv"

	gopty "github.com/aymanbagabas/go-pty"
	"github.com/wesm/middleman/internal/procutil"
)

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
