//go:build !windows

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestPOSIXCommandHintRoundTrip(t *testing.T) {
	command := quoteCommand([]string{"--config", "/tmp/forge's config.toml", "--timeout", "1m0s"})
	output, err := procutil.CommandContext(t.Context(), "sh", "-c", "set -- "+command+`; printf '%s\n' "$@"`).Output()
	require.NoError(t, err)
	require.Equal(t, "--config\n/tmp/forge's config.toml\n--timeout\n1m0s\n", string(output))
}
