package testtmux

import (
	"os"
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
