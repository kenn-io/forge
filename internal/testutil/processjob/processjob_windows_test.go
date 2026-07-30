//go:build windows

package processjob

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

const (
	processJobOwnerHelper = "KENN_FORGE_PROCESS_JOB_OWNER_HELPER"
	processJobChildHelper = "KENN_FORGE_PROCESS_JOB_CHILD_HELPER"
)

func TestContainCurrentProcessTreeStopsDetachedDescendant(t *testing.T) {
	if os.Getenv(processJobChildHelper) == "1" {
		time.Sleep(time.Hour)
		return
	}
	if os.Getenv(processJobOwnerHelper) == "1" {
		runProcessJobOwnerHelper()
	}

	require := require.New(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	owner := exec.Command(
		os.Args[0],
		"-test.run=^TestContainCurrentProcessTreeStopsDetachedDescendant$",
	)
	owner.Env = append(os.Environ(), processJobOwnerHelper+"=1")
	owner.Args = append(owner.Args, "--", pidFile)
	require.NoError(owner.Start())
	t.Cleanup(func() {
		if owner.ProcessState == nil {
			_ = owner.Process.Kill()
			_, _ = owner.Process.Wait()
		}
	})

	var childPID int
	require.Eventually(func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		childPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 5*time.Second, 25*time.Millisecond)

	require.NoError(owner.Process.Kill())
	_ = owner.Wait()
	require.Eventually(
		func() bool { return windowsProcessExited(uint32(childPID)) },
		5*time.Second,
		25*time.Millisecond,
	)
}

func runProcessJobOwnerHelper() {
	if err := ContainCurrentProcessTree(); err != nil {
		os.Exit(2)
	}
	args := os.Args
	pidFile := args[len(args)-1]
	child := exec.Command(
		os.Args[0],
		"-test.run=^TestContainCurrentProcessTreeStopsDetachedDescendant$",
	)
	child.Env = append(os.Environ(), processJobChildHelper+"=1")
	child.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP |
			windows.DETACHED_PROCESS |
			windows.CREATE_NO_WINDOW,
	}
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(
		pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600,
	); err != nil {
		_ = child.Process.Kill()
		os.Exit(2)
	}
	time.Sleep(time.Hour)
}

func windowsProcessExited(pid uint32) bool {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true
	}
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	status, err := windows.WaitForSingleObject(process, 0)
	return err == nil && status == windows.WAIT_OBJECT_0
}
