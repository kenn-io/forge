//go:build !windows

package procutil

import (
	"os"
	"os/exec"
	"path/filepath"
)

func ConfigureBackgroundCommand(*exec.Cmd) {}

func binaryPathCandidates(dir, name string) []string {
	return []string{filepath.Join(dir, name)}
}

func isExecutableCandidate(info os.FileInfo) bool {
	return info.Mode().Perm()&0o111 != 0
}
