//go:build !windows

package procutil

import (
	"os/exec"
	"path/filepath"
)

func ConfigureBackgroundCommand(*exec.Cmd) {}

func binaryPathCandidates(dir, name string) []string {
	return []string{filepath.Join(dir, name)}
}
