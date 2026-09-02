package testtmux

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadProcessStatFromProcConfirmsMissingProcess(t *testing.T) {
	_, err := readProcessStatFromProc(
		31415,
		func(string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
		func(int) error {
			return errProcessAbsent
		},
	)
	require.ErrorIs(t, err, errProcessAbsent)
}

func TestReadProcessStatFromProcPreservesMissingStatAsIndeterminate(t *testing.T) {
	statErr := &os.PathError{
		Op:   "open",
		Path: "/proc/31415/stat",
		Err:  os.ErrNotExist,
	}
	_, err := readProcessStatFromProc(
		31415,
		func(string) ([]byte, error) {
			return nil, statErr
		},
		func(int) error {
			return nil
		},
	)
	require.Same(t, statErr, err)
	require.NotErrorIs(t, err, errProcessAbsent)
}

func TestReadProcessStatFromProcTreatsVanishedTaskAsMissing(t *testing.T) {
	statErr := &os.PathError{
		Op:   "read",
		Path: "/proc/31415/stat",
		Err:  syscall.ESRCH,
	}
	_, err := readProcessStatFromProc(
		31415,
		func(string) ([]byte, error) {
			return nil, statErr
		},
		func(int) error {
			require.Fail(t, "probe must not run when the kernel already reported the task gone")
			return nil
		},
	)
	require.ErrorIs(t, err, errProcessAbsent)
}
