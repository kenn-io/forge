package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.kenn.io/forge/internal/testutil/testscript"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

// TestPruneMissingTmuxSessionsKeepsReadyWorkspacesWhenServerIsEmpty pins
// the bulk-gone case: after a machine reboot or the dedicated-socket
// upgrade, no session lives, base sessions recreate lazily on attach,
// and ready workspaces must stay ready rather than flip to an error
// whose retry force-removes worktrees. Runtime rows stay too: they hold
// the agents that recovery resumes.
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
	mgr := newWorkspaceTestManager(t, d, t.TempDir())
	mgr.SetTmuxCommand(testscript.Command(script))
	ctx := t.Context()
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
	recordRuntimeTmuxSessionForTest(
		t, d, "0000000000000009", "0000000000000009_claude",
		"claude", "forge-0000000000000009-c857d09db23e6822", time.Time{},
	)

	changed, err := mgr.PruneMissingTmuxSessions(ctx, TmuxPruneHooks{})
	require.NoError(err)
	assert.False(changed)

	ws, err := mgr.Get(ctx, "0000000000000009")
	require.NoError(err)
	require.NotNil(ws)
	assert.Equal("ready", ws.Status,
		"an empty server must not error ready workspaces")
	stored, err := d.ListWorkspaceRuntimeTmuxSessions(ctx, "0000000000000009")
	require.NoError(err)
	assert.Len(stored, 1, "an empty server must not forget runtime sessions")
}
