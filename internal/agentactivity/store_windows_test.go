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

const shareReadWrite = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE

func TestStoreRemoveWithOpenReports(t *testing.T) {
	for _, tc := range []struct {
		name           string
		share          uint32
		invalidContent bool
		otherLive      bool
		wantCleared    bool
	}{
		{name: "writable", share: shareReadWrite, otherLive: true, wantCleared: true},
		{name: "write only", share: windows.FILE_SHARE_WRITE, wantCleared: true},
		{name: "invalid", share: shareReadWrite, invalidContent: true, wantCleared: true},
		{name: "unwritable", share: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			require := require.New(t)
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			store := NewStoreWithClock(t.TempDir(), func() time.Time { return now })
			cwd := t.TempDir()
			live := []string{"runtime"}
			survivors := []Report{}
			if tc.otherLive {
				require.NoError(store.Record("claude", "live", "runtime", cwd, StateWorking))
				survivors = store.LiveReportsForWorkspace(cwd, live)
				require.Len(survivors, 1)
				require.Equal("live", survivors[0].SessionID)
			}
			require.NoError(store.Record("claude", "chat", "runtime", cwd, StateDone))
			require.Len(store.LiveReportsForWorkspace(cwd, live), len(survivors)+1)
			path := store.reportPath("claude", "chat", "runtime")
			if tc.invalidContent {
				require.NoError(os.WriteFile(path, []byte("invalid"), 0o600))
			}
			release := lockReportForTest(t, path, tc.share)

			err := store.Remove("claude", "chat", "runtime")

			assert.FileExists(path)
			if !tc.wantCleared {
				require.ErrorContains(err, path)
				assert.Len(store.LiveReportsForWorkspace(cwd, live), 1)
				release()
				reports := store.LiveReportsForWorkspace(cwd, live)
				require.Len(reports, 1)
				assert.Equal(StateDone, reports[0].State)
				return
			}
			require.NoError(err)
			assert.Equal(survivors, store.LiveReportsForWorkspace(cwd, live))
			restarted := NewStore(store.root)
			assert.Equal(survivors, restarted.LiveReportsForWorkspace(cwd, live))
			release()
			assert.Equal(survivors, restarted.LiveReportsForWorkspace(cwd, live))
			assert.NoFileExists(path)
		})
	}
}

func TestStoreRemoveWithLockedLegacyReports(t *testing.T) {
	const (
		currentMissing   = "missing"
		currentRemovable = "removable"
		currentLocked    = "locked"
	)
	for _, tc := range []struct {
		name                   string
		share                  uint32
		current                string
		wantLegacyErr          bool
		wantCurrentErr         bool
		wantVisible            State
		wantVisibleAfterUnlock State
		wantLegacyAfterUnlock  bool
	}{
		{name: "writable/missing", share: shareReadWrite, current: currentMissing},
		{name: "writable/removable", share: shareReadWrite, current: currentRemovable},
		{
			name: "unwritable/missing", share: windows.FILE_SHARE_READ, current: currentMissing,
			wantLegacyErr: true, wantVisible: StateWorking, wantVisibleAfterUnlock: StateWorking,
			wantLegacyAfterUnlock: true,
		},
		{
			name: "unwritable/removable", share: windows.FILE_SHARE_READ, current: currentRemovable,
			wantLegacyErr: true, wantVisible: StateWorking, wantVisibleAfterUnlock: StateWorking,
			wantLegacyAfterUnlock: true,
		},
		{
			name: "unwritable/locked", share: windows.FILE_SHARE_READ, current: currentLocked,
			wantLegacyErr: true, wantCurrentErr: true, wantVisible: StateDone,
			wantVisibleAfterUnlock: StateDone, wantLegacyAfterUnlock: true,
		},
		{
			name: "unreadable/missing", share: windows.FILE_SHARE_WRITE, current: currentMissing,
			wantLegacyErr: true, wantVisibleAfterUnlock: StateWorking, wantLegacyAfterUnlock: true,
		},
		{
			name: "unreadable/removable", share: windows.FILE_SHARE_WRITE, current: currentRemovable,
			wantLegacyErr: true, wantVisibleAfterUnlock: StateWorking, wantLegacyAfterUnlock: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			require := require.New(t)
			store := NewStore(t.TempDir())
			cwd := t.TempDir()
			legacy := writeLegacyReportForTest(t, store, cwd)
			path := store.reportPath("claude", "chat", "runtime")
			if tc.current != currentMissing {
				require.NoError(store.Record("claude", "chat", "runtime", cwd, StateDone))
			}
			require.NotEmpty(store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
			releaseLegacy := lockReportForTest(t, legacy, tc.share)
			releaseCurrent := func() {}
			if tc.current == currentLocked {
				releaseCurrent = lockReportForTest(t, path, tc.share)
			}

			err := endChatSessionForTest(store, "runtime")

			if tc.wantLegacyErr {
				require.ErrorContains(err, legacy)
			} else {
				require.NoError(err)
			}
			if tc.wantCurrentErr {
				require.ErrorContains(err, path)
			} else if err != nil {
				assert.NotContains(err.Error(), path)
			}
			assert.FileExists(legacy)
			if tc.current == currentLocked {
				assert.FileExists(path)
			} else {
				assert.NoFileExists(path)
			}
			// While a lock blocks reads, visibility depends on whether the scan cache still holds the report.
			if tc.share&windows.FILE_SHARE_READ != 0 {
				assert.Equal(tc.wantVisible, visibleStateForTest(t, store, cwd))
			}
			releaseCurrent()
			releaseLegacy()
			assert.Equal(tc.wantVisibleAfterUnlock, visibleStateForTest(t, store, cwd))
			if tc.wantLegacyAfterUnlock {
				assert.FileExists(legacy)
			} else {
				assert.NoFileExists(legacy)
			}
		})
	}
}

func TestStoreSessionEndAfterLegacyReportEmptied(t *testing.T) {
	t.Run("repeated", func(t *testing.T) {
		t.Parallel()
		store, cwd, legacy, release := emptyLockedLegacyReportForTest(t)
		require.NoError(t, endChatSessionForTest(store, "runtime"))
		release()
		assert.Empty(t, store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
		assert.NoFileExists(t, legacy)
	})
	t.Run("other runtime", func(t *testing.T) {
		t.Parallel()
		store, cwd, legacy, release := emptyLockedLegacyReportForTest(t)
		require.NoError(t, store.Record("claude", "chat", "runtime-b", cwd, StateDone))
		require.NoError(t, endChatSessionForTest(store, "runtime-b"))
		assert.Empty(t, store.LiveReportsForWorkspace(cwd, []string{"runtime", "runtime-b"}))
		release()
		assert.Empty(t, store.LiveReportsForWorkspace(cwd, []string{"runtime", "runtime-b"}))
		assert.NoFileExists(t, legacy)
	})
	t.Run("relocked without write sharing", func(t *testing.T) {
		t.Parallel()
		store, cwd, legacy, release := emptyLockedLegacyReportForTest(t)
		release()
		release = lockReportForTest(t, legacy, windows.FILE_SHARE_READ)
		require.NoError(t, endChatSessionForTest(store, "runtime"))
		release()
		assert.Empty(t, store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
		assert.NoFileExists(t, legacy)
	})
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

// emptyLockedLegacyReportForTest ends a session whose legacy report is held open without
// delete sharing, leaving the report emptied but present and still locked.
func emptyLockedLegacyReportForTest(t *testing.T) (*Store, string, string, func()) {
	t.Helper()
	store := NewStore(t.TempDir())
	cwd := t.TempDir()
	legacy := writeLegacyReportForTest(t, store, cwd)
	require.NotEmpty(t, store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
	release := lockReportForTest(t, legacy, shareReadWrite)
	require.NoError(t, endChatSessionForTest(store, "runtime"))
	require.FileExists(t, legacy)
	require.Empty(t, store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
	return store, cwd, legacy, release
}

// writeLegacyReportForTest saves a working report under the pre-terminal filename, dated
// before any report the test records so newer reports win deterministically.
func writeLegacyReportForTest(t *testing.T, store *Store, cwd string) string {
	t.Helper()
	legacy := store.legacyReportPath("claude", "chat")
	data, err := json.Marshal(Report{
		Agent: "claude", SessionID: "chat", RuntimeSessionKey: "runtime", CWD: cwd,
		State: StateWorking, UpdatedAt: time.Now().UTC().Add(-time.Minute),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(legacy, data, 0o600))
	return legacy
}

func endChatSessionForTest(store *Store, runtimeSessionKey string) error {
	return store.HandleEvent(
		"claude", HookEvent{SessionID: "chat", HookEventName: "SessionEnd"}, runtimeSessionKey,
	)
}

func visibleStateForTest(t *testing.T, store *Store, cwd string) State {
	t.Helper()
	reports := store.LiveReportsForWorkspace(cwd, []string{"runtime"})
	require.LessOrEqual(t, len(reports), 1)
	if len(reports) == 0 {
		return ""
	}
	return reports[0].State
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
