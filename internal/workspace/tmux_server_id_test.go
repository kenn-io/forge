package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Some tmux builds replace tabs in format output, so the server lookup must
// still find a session's server when they do.
func TestTmuxServerIDWithSessionSurvivesTabSanitization(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	script := filepath.Join(t.TempDir(), "fake-tmux")
	require.NoError(os.WriteFile(script, []byte(`#!/bin/sh
fmt=''
prev=''
for a in "$@"; do
  if [ "$prev" = "-F" ]; then fmt="$a"; fi
  prev="$a"
done
for name in forge-aaaaaaaaaaaaaaaa forge-bbbbbbbbbbbbbbbb; do
  printf '%s\n' "$fmt" | sed -e "s|#{session_name}|$name|" -e 's|#{pid}|123|' -e 's|#{start_time}|456|' | tr '\t' '_'
done
`), 0o755))
	mgr := newWorkspaceTestManager(t, openTestDB(t), t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	server, err := mgr.TmuxServerIDWithSession(t.Context(), "forge-bbbbbbbbbbbbbbbb")
	require.NoError(err)
	assert.Equal(t, "123:456", server)
	server, err = mgr.TmuxServerIDWithSession(t.Context(), "forge-cccccccccccccccc")
	require.NoError(err)
	assert.Empty(t, server, "a server without the session is not reported")
}
