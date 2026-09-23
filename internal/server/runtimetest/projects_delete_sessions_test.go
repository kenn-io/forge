package runtimetest

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertFakeTmuxKilledSession(
	t *testing.T,
	recordPath string,
	tmuxSession string,
) {
	t.Helper()
	record, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	assert.Contains(
		t, string(record), "kill-session\x00-t\x00"+tmuxSession,
	)
}
