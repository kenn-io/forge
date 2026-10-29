package testtmux

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestOwnerCleanupAfterStartupMarkerWriteFails(t *testing.T) {
	const admissionEnv = "KENN_FORGE_TEST_TMUX_WRITE_FAILURE_ADMISSION"
	if admissionDir := os.Getenv(admissionEnv); admissionDir != "" {
		require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{}))
		os.Exit(runWrappedTmux(wrapperMetadata{
			TmuxPath:     "/bin/true",
			AdmissionDir: admissionDir,
		}, []string{"new-session"}))
	}

	owner, err := newAt(filepath.Join(t.TempDir(), "root"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = owner.Cleanup() })
	command := procutil.Command(os.Args[0], "-test.run=^TestOwnerCleanupAfterStartupMarkerWriteFails$")
	command.Env = append(os.Environ(), admissionEnv+"="+owner.admissionDir)
	output, err := command.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "record private tmux startup:")
	require.NoError(t, owner.Cleanup())
}
