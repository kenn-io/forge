//go:build windows

package localruntime

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"go.kenn.io/forge/internal/procutil"
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

// The agent joins its parent's console when the parent has one, and gets a
// console with no window when the parent has none.
func TestConfigureACPProcessSharesParentConsole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags uint32
		want  string
	}{
		{name: "parent console", want: "window=false shared=true"},
		{name: "no parent console", flags: windows.DETACHED_PROCESS, want: "window=false shared=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := windowsConsoleHelper(t, "parent")
			parent.SysProcAttr.CreationFlags |= tc.flags
			out, err := parent.Output()
			require.NoError(t, err, string(out))
			assert.True(t, strings.HasPrefix(string(out), tc.want+"\n"), string(out))
		})
	}
}

func TestWindowsConsoleHelper(t *testing.T) {
	switch os.Getenv("KENN_FORGE_WINDOWS_CONSOLE_HELPER") {
	case "parent":
		child := windowsConsoleHelper(t, "child")
		configureACPProcess(child)
		out, err := child.Output()
		require.NoError(t, err, string(out))
		fields := strings.Fields(string(out))
		require.NotEmpty(t, fields)
		shared := slices.Contains(fields, strconv.Itoa(os.Getpid()))
		_, _ = fmt.Printf("%s shared=%v\n", fields[0], shared)
	case "child":
		kernel32 := windows.NewLazySystemDLL("kernel32.dll")
		// A console created with CREATE_NO_WINDOW has no window; any other new
		// console has one, even when it starts hidden.
		window, _, _ := kernel32.NewProc("GetConsoleWindow").Call()
		_, _ = fmt.Printf("window=%v\n", window != 0)
		pids := make([]uint32, 64)
		n, _, _ := kernel32.NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
		for _, pid := range pids[:min(int(n), len(pids))] {
			_, _ = fmt.Println(pid)
		}
	}
}

// windowsConsoleHelper starts this test binary the way procutil starts agents.
func windowsConsoleHelper(t *testing.T, mode string) *exec.Cmd {
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := procutil.CommandContext(t.Context(), executable, "-test.run=^TestWindowsConsoleHelper$")
	cmd.Env = append(os.Environ(), "KENN_FORGE_WINDOWS_CONSOLE_HELPER="+mode)
	return cmd
}
