// Package testsignal installs bounded process-signal cleanup for test binaries
// that own external resources which outlive the Go process.
package testsignal

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Install registers cleanup for interrupt and termination signals. The returned
// cleanup function is shared with the normal TestMain exit path and runs at most
// once. stop removes the signal handler after m.Run completes.
func Install(
	cleanup func() error,
	report func(error),
) (runCleanup func() error, stop func()) {
	var cleanupOnce sync.Once
	var cleanupErr error
	runCleanup = func() error {
		cleanupOnce.Do(func() {
			if cleanup != nil {
				cleanupErr = cleanup()
			}
		})
		return cleanupErr
	}

	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case received := <-signals:
			if err := runCleanup(); err != nil && report != nil {
				report(err)
			}
			signal.Stop(signals)
			if received == os.Interrupt {
				os.Exit(130)
			}
			os.Exit(143)
		case <-done:
		}
	}()

	var stopOnce sync.Once
	stop = func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			close(done)
		})
	}
	return runCleanup, stop
}
