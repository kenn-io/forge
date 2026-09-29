//go:build darwin || linux

package server

import (
	"time"

	"golang.org/x/sys/unix"

	"go.kenn.io/forge/internal/procutil"
)

func init() {
	procutil.SetDefaultLimiterForTest(
		procutil.NewLimiterWithAcquireTimeout(128, 15*time.Second),
	)
	raiseTestRLimit(unix.RLIMIT_NPROC)
	raiseTestRLimit(unix.RLIMIT_NOFILE)
}

func raiseTestRLimit(resource int) {
	var limit unix.Rlimit
	if err := unix.Getrlimit(resource, &limit); err != nil {
		return
	}
	if limit.Cur >= limit.Max {
		return
	}
	limit.Cur = limit.Max
	_ = unix.Setrlimit(resource, &limit)
}
