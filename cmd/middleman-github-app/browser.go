package main

import (
	"fmt"
	"os/exec"
	"runtime"

	"go.kenn.io/middleman/internal/procutil"
)

// openInBrowser opens url in the user's default browser. Callers
// always print the URL too, so a failed or headless open still leaves
// a manual path.
func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = procutil.Command("open", url)
	case "windows":
		cmd = procutil.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = procutil.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening browser: %w", err)
	}
	// Release the child; xdg-open and friends exit immediately.
	go func() { _ = cmd.Wait() }()
	return nil
}
