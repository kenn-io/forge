//go:build windows

package localruntime

import (
	"strconv"

	"github.com/wesm/middleman/internal/procutil"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	cmd := procutil.Command(
		"powershell",
		"-NoProfile",
		"-Command",
		"if (Get-Process -Id "+strconv.Itoa(pid)+" -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }",
	)
	return cmd.Run() == nil
}
