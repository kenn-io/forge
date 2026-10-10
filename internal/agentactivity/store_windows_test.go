package agentactivity

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestStoreRemoveReturnsLockedReportErrors(t *testing.T) {
	for _, current := range []string{"missing", "removable", "locked"} {
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
			lockReportForTest(t, legacy)
			path := store.reportPath("claude", "chat", "runtime")
			if current != "missing" {
				require.NoError(store.Record("claude", "chat", "runtime", workspace, StateDone))
			}
			if current == "locked" {
				lockReportForTest(t, path)
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

func TestStoreRemoveReturnsLegacyOpenError(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	legacy := store.legacyReportPath("claude", "chat")
	require.NoError(t, os.WriteFile(legacy, []byte("invalid"), 0o600))
	name, err := windows.UTF16PtrFromString(legacy)
	require.NoError(t, err)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, windows.CloseHandle(handle)) })

	require.ErrorContains(t, store.Remove("claude", "chat", "runtime"), legacy)
	assert.FileExists(t, legacy)
}

func lockReportForTest(t *testing.T, path string) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)
	// Readers remain allowed while Windows denies deletion of the report.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, windows.CloseHandle(handle)) })
}
