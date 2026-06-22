//go:build !windows

package ptyowner

import (
	"os/signal"
	"syscall"
)

func ignorePtyOwnerHangupForTest() {
	signal.Ignore(syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
}
