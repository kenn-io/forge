package workspaceapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	"golang.org/x/sys/windows"
)

func TestWorkspaceTmuxPruneRetriesLockedActivityReport(t *testing.T) {
	t.Parallel()
	for _, cleanup := range []string{"prune", "remove"} {
		t.Run(cleanup, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			cwd := t.TempDir()
			require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
				ID: "workspace", Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeAdHoc,
				ItemKey: db.AdHocWorkspaceItemKey("work/cleanup"), GitHeadRef: "work/cleanup",
				WorkspaceBranch: "work/cleanup", WorktreePath: cwd, Status: "ready",
			}))
			require.NoError(database.UpsertWorkspaceRuntimeSession(t.Context(), &db.WorkspaceRuntimeSession{
				WorkspaceID: "workspace", SessionKey: "runtime", TargetKey: "worker",
				Kind: "agent", Scope: "session", TmuxSession: "missing",
			}))
			root := t.TempDir()
			activity := agentactivity.NewStore(root)
			require.NoError(activity.Record("claude", "chat", "runtime", cwd, agentactivity.StateWorking))
			entries, err := os.ReadDir(root)
			require.NoError(err)
			require.Len(entries, 1)
			path := filepath.Join(root, entries[0].Name())
			name, err := windows.UTF16PtrFromString(path)
			require.NoError(err)
			handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			require.NoError(err)
			t.Cleanup(func() {
				if handle != windows.InvalidHandle {
					require.NoError(windows.CloseHandle(handle))
				}
			})
			manager := workspace.NewManager(database, t.TempDir())
			cmd, err := exec.LookPath("cmd.exe")
			require.NoError(err)
			// A successful empty listing represents a tmux server with no sessions.
			manager.SetTmuxCommand([]string{cmd, "/c", "exit", "0"})
			broadcasts := 0
			handler := New(Deps{DB: database, Workspaces: manager, AgentActivity: activity, Broadcast: func(Event) uint64 {
				broadcasts++
				return uint64(broadcasts)
			}})
			if cleanup == "remove" {
				handler.removeAgentActivityRuntimeSession("runtime")
				require.NoError(database.DeleteWorkspaceRuntimeSession(t.Context(), "workspace", "runtime"))
			}

			handler.runWorkspaceTmuxPrune(t.Context())
			stored, err := database.ListAllWorkspaceRuntimeSessions(t.Context())
			require.NoError(err)
			assert.Empty(stored)
			assert.FileExists(path)
			wantBroadcasts := 1
			if cleanup == "remove" {
				wantBroadcasts = 0
			}
			assert.Equal(wantBroadcasts, broadcasts)
			assert.NotEmpty(handler.agentActivityCleanupError)
			previousError := handler.agentActivityCleanupError
			handler.runWorkspaceTmuxPrune(t.Context())
			assert.Equal(previousError, handler.agentActivityCleanupError)
			require.NoError(windows.CloseHandle(handle))
			handle = windows.InvalidHandle

			handler.runWorkspaceTmuxPrune(t.Context())
			assert.NoFileExists(path)
			assert.Equal(wantBroadcasts, broadcasts)
			assert.Empty(handler.agentActivityCleanupError)
			require.NoError(activity.Record("claude", "other", "orphan", cwd, agentactivity.StateWorking))
			handler.runWorkspaceTmuxPrune(t.Context())
			entries, err = os.ReadDir(root)
			require.NoError(err)
			assert.Len(entries, 1)
		})
	}
}
