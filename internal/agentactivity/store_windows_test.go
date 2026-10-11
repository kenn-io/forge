package agentactivity

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestStoreRemoveWithOpenReports(t *testing.T) {
	for _, tc := range []struct {
		name    string
		share   uint32
		invalid bool
	}{
		{"writable", windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE, false},
		{"write only", windows.FILE_SHARE_WRITE, false},
		{"invalid", windows.FILE_SHARE_WRITE, true},
		{"unwritable", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			require := require.New(t)
			store := NewStore(t.TempDir())
			cwd := t.TempDir()
			require.NoError(store.Record("claude", "chat", "runtime", cwd, StateDone))
			require.Len(store.LiveReportsForWorkspace(cwd, []string{"runtime"}), 1)
			survivors := []Report{}
			if tc.name == "writable" {
				require.NoError(store.Record("claude", "live", "runtime", cwd, StateWorking))
				reports := store.LiveReportsForWorkspace(cwd, []string{"runtime"})
				require.Len(reports, 2)
				survivors = reports[:1]
				require.Equal("live", survivors[0].SessionID)
			}
			path := store.reportPath("claude", "chat", "runtime")
			if tc.invalid {
				require.NoError(os.WriteFile(path, []byte("invalid"), 0o600))
			}
			release := lockReportForTest(t, path, tc.share)
			writable := tc.share&windows.FILE_SHARE_WRITE != 0
			err := store.Remove("claude", "chat", "runtime")
			if writable {
				require.NoError(err)
				assert.Equal(survivors, store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
				store = NewStore(store.root)
				assert.Equal(survivors, store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
			} else {
				require.ErrorContains(err, path)
				assert.Len(store.LiveReportsForWorkspace(cwd, []string{"runtime"}), 1)
			}
			assert.FileExists(path)
			release()
			reports := store.LiveReportsForWorkspace(cwd, []string{"runtime"})
			if writable {
				assert.Equal(survivors, reports)
				assert.NoFileExists(path)
			} else {
				require.Len(reports, 1)
				assert.Equal(StateDone, reports[0].State)
			}
		})
	}
}

func TestStoreRemoveWithLockedLegacyReports(t *testing.T) {
	for _, tc := range []struct {
		name  string
		share uint32
	}{
		{"writable", windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE},
		{"unwritable", windows.FILE_SHARE_READ},
		{"unreadable", windows.FILE_SHARE_WRITE},
	} {
		currents := []string{"missing", "removable"}
		switch tc.name {
		case "writable":
			currents = append(currents, "repeated", "runtime-b", "relocked")
		case "unwritable":
			currents = append(currents, "locked")
		}
		for _, current := range currents {
			t.Run(tc.name+"/"+current, func(t *testing.T) {
				t.Parallel()
				assert := assert.New(t)
				require := require.New(t)
				store := NewStore(t.TempDir())
				cwd := t.TempDir()
				legacy := store.legacyReportPath("claude", "chat")
				data, err := json.Marshal(Report{Agent: "claude", SessionID: "chat", RuntimeSessionKey: "runtime", CWD: cwd, State: StateWorking, UpdatedAt: time.Now().UTC()})
				require.NoError(err)
				require.NoError(os.WriteFile(legacy, data, 0o600))
				path := store.reportPath("claude", "chat", "runtime")
				if current == "removable" || current == "locked" {
					require.NoError(store.Record("claude", "chat", "runtime", cwd, StateDone))
				}
				require.NotEmpty(store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
				releaseLegacy := lockReportForTest(t, legacy, tc.share)
				releaseCurrent := func() {}
				if current == "locked" {
					releaseCurrent = lockReportForTest(t, path, tc.share)
				}
				err = store.HandleEvent("claude", HookEvent{SessionID: "chat", HookEventName: "SessionEnd"}, "runtime")
				if tc.name == "writable" {
					require.NoError(err)
				} else {
					require.ErrorContains(err, legacy)
					if current == "locked" && tc.name == "unwritable" {
						require.ErrorContains(err, path)
					}
				}
				if current == "removable" {
					assert.Nil(store.cacheFiles)
					assert.Nil(store.cacheReports)
				}
				assert.FileExists(legacy)
				if current != "locked" {
					assert.NoFileExists(path)
				}
				if tc.name == "writable" {
					assert.Empty(store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
					switch current {
					case "repeated":
						require.NoError(store.HandleEvent("claude", HookEvent{SessionID: "chat", HookEventName: "SessionEnd"}, "runtime"))
					case "runtime-b":
						require.NoError(store.Record("claude", "chat", "runtime-b", cwd, StateDone))
						require.NoError(store.HandleEvent("claude", HookEvent{SessionID: "chat", HookEventName: "SessionEnd"}, "runtime-b"))
						assert.Empty(store.LiveReportsForWorkspace(cwd, []string{"runtime", "runtime-b"}))
					case "relocked":
						releaseLegacy()
						releaseLegacy = lockReportForTest(t, legacy, windows.FILE_SHARE_READ)
						require.NoError(store.HandleEvent("claude", HookEvent{SessionID: "chat", HookEventName: "SessionEnd"}, "runtime"))
					}
				}
				releaseCurrent()
				releaseLegacy()
				if tc.name == "writable" {
					assert.Empty(store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
					assert.NoFileExists(legacy)
				} else {
					assert.NotEmpty(store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
				}
			})
		}
	}
}

func TestReportLockError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		lock bool
	}{
		{windows.ERROR_SHARING_VIOLATION, true},
		{windows.ERROR_ACCESS_DENIED, true},
		{windows.ERROR_DISK_FULL, false},
		{os.ErrNotExist, false},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.lock, isReportLockError(fmt.Errorf("remove report: %w", tc.err)))
		})
	}
}

func lockReportForTest(t *testing.T, path string, share uint32) func() {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	require.NoError(t, err)
	released := false
	release := func() {
		if !released {
			require.NoError(t, windows.CloseHandle(handle))
			released = true
		}
	}
	t.Cleanup(release)
	return release
}
