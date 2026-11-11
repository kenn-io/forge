//go:build !windows

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestPOSIXCommandHintRoundTrip(t *testing.T) {
	command := quoteCommand([]string{"kenn-forge", "--config", "#forge.toml", "--timeout", "1m0s", "/tmp/forge's config.toml", "", "$HOME; echo wrong"})
	output, err := procutil.CommandContext(t.Context(), "sh", "-c", "set -- "+command+`; printf '%s\n' "$@"`).Output()
	require.NoError(t, err)
	require.Equal(t, "kenn-forge\n--config\n#forge.toml\n--timeout\n1m0s\n/tmp/forge's config.toml\n\n$HOME; echo wrong\n", string(output))
}
