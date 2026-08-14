//go:build !windows

package testtmux

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTmuxProcessInventoryRejectsIndeterminateIdentity(t *testing.T) {
	output := fmt.Appendf(nil,
		"31415 %d tmux -S /tmp/private/tmux.sock\n", os.Getuid(),
	)

	_, err := parseTmuxProcesses(output, func(int) (string, error) {
		return "", errors.New("transient identity lookup failure")
	})
	require.Error(t, err)
}

func TestStopProcessRejectsIndeterminateIdentity(t *testing.T) {
	err := stopProcessWithLookup(
		31415,
		"startup-identity",
		func(int) (string, error) {
			return "", errors.New("transient identity lookup failure")
		},
	)
	require.Error(t, err)
}
