package workspace

import (
	"fmt"
	"os"
	"testing"

	"go.kenn.io/forge/internal/testutil/testsignal"
	"go.kenn.io/forge/internal/testutil/testtmux"
)

var privateTmuxOwner *testtmux.Owner

func TestMain(m *testing.M) {
	var err error
	if testtmux.Supported() {
		privateTmuxOwner, err = testtmux.New()
		if err != nil {
			fmt.Fprintf(os.Stderr, "initialize private test tmux owner: %v\n", err)
			os.Exit(1)
		}
	}
	runCleanup, stopSignalCleanup := testsignal.Install(
		func() error {
			if privateTmuxOwner == nil {
				return nil
			}
			return privateTmuxOwner.Cleanup()
		},
		func(cleanupErr error) {
			fmt.Fprintf(os.Stderr, "cleanup private test tmux servers: %v\n", cleanupErr)
		},
	)
	code := m.Run()
	if cleanupErr := runCleanup(); cleanupErr != nil {
		fmt.Fprintf(os.Stderr, "cleanup private test tmux servers: %v\n", cleanupErr)
		if code == 0 {
			code = 1
		}
	}
	stopSignalCleanup()
	os.Exit(code)
}
