package testsignal

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestInstallRunsCleanupBeforeSIGTERMExit(t *testing.T) {
	if os.Getenv("KENN_FORGE_TEST_SIGNAL_HELPER") == "1" {
		cleanupPath := os.Getenv("KENN_FORGE_TEST_SIGNAL_CLEANUP")
		Install(func() error {
			return os.WriteFile(cleanupPath, []byte("cleaned\n"), 0o600)
		}, nil)
		if err := os.WriteFile(
			os.Getenv("KENN_FORGE_TEST_SIGNAL_READY"),
			[]byte("ready\n"),
			0o600,
		); err != nil {
			os.Exit(2)
		}
		select {}
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support sending SIGTERM to the helper process")
	}

	dir := t.TempDir()
	assert := assert.New(t)
	require := require.New(t)
	readyPath := dir + "/ready"
	cleanupPath := dir + "/cleanup"
	cmd := procutil.Command(os.Args[0], "-test.run=^TestInstallRunsCleanupBeforeSIGTERMExit$")
	cmd.Env = append(os.Environ(),
		"KENN_FORGE_TEST_SIGNAL_HELPER=1",
		"KENN_FORGE_TEST_SIGNAL_READY="+readyPath,
		"KENN_FORGE_TEST_SIGNAL_CLEANUP="+cleanupPath,
	)
	require.NoError(cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			require.NoError(err)
		}
		if time.Now().After(deadline) {
			require.FailNow("signal helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.NoError(cmd.Process.Signal(syscall.SIGTERM))
	err := cmd.Wait()
	var exitErr *exec.ExitError
	require.ErrorAs(err, &exitErr)
	assert.Equal(143, exitErr.ExitCode())
	content, readErr := os.ReadFile(cleanupPath)
	require.NoError(readErr)
	assert.Equal("cleaned\n", string(content))
}
