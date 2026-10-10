package agentactivity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestStoreRemoveReturnsLockedReportErrors(t *testing.T) {
	for _, current := range []string{"missing", "removable", "locked", "legacy-open"} {
		t.Run(current, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			require := require.New(t)
			store := NewStore(t.TempDir())
			workspace := t.TempDir()
			legacy := store.legacyReportPath("claude", "chat")
			data, err := json.Marshal(Report{Agent: "claude", SessionID: "chat", RuntimeSessionKey: "runtime", CWD: workspace, State: StateWorking, UpdatedAt: time.Now().UTC()})
			require.NoError(err)
			require.NoError(os.WriteFile(legacy, data, 0o600))
			path := store.reportPath("claude", "chat", "runtime")
			if current != "missing" && current != "legacy-open" {
				require.NoError(store.Record("claude", "chat", "runtime", workspace, StateDone))
			}
			require.NotEmpty(store.LiveReportsForWorkspace(workspace, []string{"runtime"}))
			share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
			if current == "legacy-open" {
				share = 0
			}
			lockReportForTest(t, legacy, share)
			if current == "locked" {
				lockReportForTest(t, path, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
			}
			require.NotEmpty(store.LiveReportsForWorkspace(workspace, []string{"runtime"}))

			err = store.HandleEvent("claude", HookEvent{SessionID: "chat", HookEventName: "SessionEnd"}, "runtime")
			require.Error(err)
			assert.Contains(err.Error(), legacy)
			assert.FileExists(legacy)
			if current == "locked" {
				assert.Contains(err.Error(), path)
				assert.FileExists(path)
			} else {
				assert.NoFileExists(path)
			}
			if current == "removable" {
				assert.Nil(store.cacheFiles)
				assert.Nil(store.cacheReports)
			}
		})
	}
}

func TestStoreRuntimeCleanupRetriesLockedReport(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"remove", "retain"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			store := NewStore(t.TempDir())
			workspace := t.TempDir()
			require.NoError(t, store.Record("claude", "chat", "runtime", workspace, StateWorking))
			require.Len(t, store.LiveReportsForWorkspace(workspace, []string{"runtime"}), 1)
			path := store.reportPath("claude", "chat", "runtime")
			unlock := lockReportForTest(t, path, 0)
			cleanup := func() error { return store.RemoveRuntimeSession("runtime") }
			if method == "retain" {
				cleanup = func() error { return store.RetainRuntimeSessions(nil) }
			}

			assert.Len(t, store.LiveReportsForWorkspace(workspace, []string{"runtime"}), 1)
			require.ErrorContains(t, cleanup(), path)
			require.ErrorContains(t, cleanup(), path)
			assert.FileExists(t, path)
			unlock()
			require.NoError(t, cleanup())
			assert.NoFileExists(t, path)
		})
	}
}

func lockReportForTest(t *testing.T, path string, share uint32) func() {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	require.NoError(t, err)
	unlock := func() {
		if handle != windows.InvalidHandle {
			require.NoError(t, windows.CloseHandle(handle))
			handle = windows.InvalidHandle
		}
	}
	t.Cleanup(unlock)
	return unlock
}

func TestStoreRuntimeCleanupReturnsReadDirErrors(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"remove", "retain"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "reports")
			store := NewStore(root)
			cleanup := func() error { return store.RemoveRuntimeSession("runtime") }
			if method == "retain" {
				cleanup = func() error { return store.RetainRuntimeSessions(nil) }
			}
			require.NoError(t, cleanup())
			require.NoError(t, os.Mkdir(root, 0o700))
			lockReportForTest(t, root, 0)
			require.ErrorContains(t, cleanup(), root)
		})
	}
}
