package workspace

import (
	"context"
	"path/filepath"
	"testing"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"os"
)

// TestPruneMissingTmuxSessionsKeepsReadyWorkspacesWhenServerIsEmpty pins
// the bulk-gone case: after a machine reboot or the dedicated-socket
// upgrade, no session lives, base sessions recreate lazily on attach,
// and ready workspaces must stay ready rather than flip to an error
// whose retry force-removes worktrees.
func TestPruneMissingTmuxSessionsKeepsReadyWorkspacesWhenServerIsEmpty(
	t *testing.T,
) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		"TMUX_RECORD=" + shellquote.Join(record) + "\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    echo "no server running on /tmp/sock" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})
	ctx := context.Background()
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "0000000000000009",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   9,
		GitHeadRef:   "feature/upgrade",
		WorktreePath: filepath.Join(t.TempDir(), "wt"),
		TmuxSession:  "kenn-forge-0000000000000009",
		Status:       "ready",
	}))

	changed, err := mgr.PruneMissingTmuxSessions(ctx)
	require.NoError(err)
	assert.False(changed)

	ws, err := mgr.Get(ctx, "0000000000000009")
	require.NoError(err)
	require.NotNil(ws)
	assert.Equal("ready", ws.Status,
		"an empty server must not error ready workspaces")
}
