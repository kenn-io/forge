package workspaceapi

import (
	"fmt"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"os"
	"testing"

	"go.kenn.io/forge/internal/testutil/gitsafe"
)

var privateTmuxOwner *testtmux.Owner

func TestMain(m *testing.M) {
	if code, ok := testtmux.CommandWrapperExitCode(); ok {
		os.Exit(code)
	}
	if testtmux.Supported() {
		var err error
		privateTmuxOwner, err = testtmux.New()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	code := gitsafe.RunIsolatedMain(m)
	if privateTmuxOwner != nil {
		if err := privateTmuxOwner.Cleanup(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}
