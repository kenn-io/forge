package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestWindowsCommandHint(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	for _, path := range []string{`C:\Program Files\forge.toml`, `C:\team&tools\forge.toml`, `C:\normal\forge.toml`, `C:\%FORGE_COMMAND_RECORDER%\forge.toml`, `C:\program files\%FORGE_COMMAND_RECORDER%\forge.toml`} {
		t.Run(path, func(t *testing.T) {
			command := quoteCommand([]string{exe, "-test.run=^TestWindowsCommandRecorder$", "--", "--config", path, "--timeout", "1m0s"})
			cmd := procutil.CommandContext(t.Context(), "cmd.exe", "/d", "/c", command)
			cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + command + `"`}
			cmd.Env = append(os.Environ(), "FORGE_COMMAND_RECORDER=1")
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			assert.Contains(t, string(output), fmt.Sprintf(`ARGS:%s`, mustJSON(t, []string{"--config", path, "--timeout", "1m0s"})))
		})
	}
}

func TestWindowsCommandRecorder(t *testing.T) {
	if os.Getenv("FORGE_COMMAND_RECORDER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			fmt.Printf("ARGS:%s\n", mustJSON(t, os.Args[i+1:]))
			return
		}
	}
	t.Fatal("missing argument separator")
}

func mustJSON(t *testing.T, value []string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return strings.TrimSpace(string(encoded))
}
