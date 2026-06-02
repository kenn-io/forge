//go:build windows

package localruntime

import "os"

func terminateSessionProcess(process *os.Process) error {
	return process.Kill()
}

func killSessionProcess(process *os.Process) error {
	return process.Kill()
}
