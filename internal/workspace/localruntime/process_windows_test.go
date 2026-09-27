//go:build windows

package localruntime

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestWindowsACPProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("KENN_FORGE_WINDOWS_PROCESS_TREE_HELPER")
	if mode == "" {
		return
	}
	if mode == "child" {
		for {
			time.Sleep(time.Second)
		}
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	child := exec.CommandContext(t.Context(), executable, "-test.run=^TestWindowsACPProcessTreeHelper$")
	child.Env = append(os.Environ(), "KENN_FORGE_WINDOWS_PROCESS_TREE_HELPER=child")
	require.NoError(t, child.Start())
	_, _ = os.Stdout.WriteString(strconv.Itoa(child.Process.Pid) + "\n")
	require.NoError(t, child.Wait())
}

func TestKillSessionProcessStopsWindowsDescendants(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	parent := exec.CommandContext(t.Context(), executable, "-test.run=^TestWindowsACPProcessTreeHelper$")
	parent.Env = append(os.Environ(), "KENN_FORGE_WINDOWS_PROCESS_TREE_HELPER=parent")
	stdout, err := parent.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, parent.Start())
	grandchildLine, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	grandchildPID, err := strconv.Atoi(strings.TrimSpace(grandchildLine))
	require.NoError(t, err)

	require.NoError(t, killSessionProcess(parent.Process))
	_ = parent.Wait()
	require.Eventually(t, func() bool {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(grandchildPID))
		if err != nil {
			return true
		}
		defer windows.CloseHandle(handle)
		var code uint32
		return windows.GetExitCodeProcess(handle, &code) != nil || code != 259 // STILL_ACTIVE from the Windows process API.
	}, 5*time.Second, 20*time.Millisecond)
}
