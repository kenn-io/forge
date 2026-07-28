package ptyowner

import (
	"fmt"
	"os"
	"testing"

	"go.kenn.io/middleman/internal/testutil/processjob"
)

func TestMain(m *testing.M) {
	if os.Getenv("MIDDLEMAN_PTYOWNER_CLOSE_PTY_HELPER") == "1" ||
		os.Getenv("MIDDLEMAN_PTYOWNER_TEST_HELPER") == "1" {
		os.Exit(m.Run())
	}
	if err := processjob.ContainCurrentProcessTree(); err != nil {
		fmt.Fprintf(os.Stderr, "contain pty owner test process tree: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
