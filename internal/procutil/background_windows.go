//go:build windows

package procutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// ConfigureBackgroundCommand prevents non-interactive helper processes from
// flashing console windows when tests run under a GUI host on Windows.
func ConfigureBackgroundCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

func binaryPathCandidates(dir, name string) []string {
	candidates := []string{filepath.Join(dir, name)}
	if filepath.Ext(name) != "" {
		return candidates
	}
	for _, ext := range filepath.SplitList(os.Getenv("PATHEXT")) {
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		candidates = append(candidates, filepath.Join(dir, name+ext))
	}
	return candidates
}

func isExecutableCandidate(os.FileInfo) bool {
	return true
}
