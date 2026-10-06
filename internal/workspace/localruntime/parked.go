package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/kit/atomicfile"
)

// Park stops a pty-owner session and keeps its row restorable. It writes the
// stop mark, an empty file per session key, first, so nothing stops unmarked.
// An ACP owner stops its agent, which runs in its own process group, and keeps
// the saved session; any other session's process tree is killed so no clean
// exit, such as Claude's SessionEnd hook, deletes its activity report.
// Detaching first keeps the exit from counting as one this manager saw.
func (m *Manager) Park(ctx context.Context, workspaceID, sessionKey string) error {
	if err := m.markStopped(sessionKey); err != nil {
		return err
	}
	// Attached panes reconnect, as after a daemon restart, instead of closing.
	if s, ok := m.session(workspaceID, sessionKey); ok {
		s.markRecoverableDetach()
	}
	if err := m.Detach(workspaceID, sessionKey); err != nil && !errors.Is(err, ErrSessionNotFound) {
		return err
	}
	if err := m.callACPOwner(ctx, sessionKey, "ACP.Park"); err != nil {
		return err
	}
	if m.ptyOwnerRuntime == nil {
		return nil
	}
	return m.ptyOwnerRuntime.Stop(ctx, sessionKey)
}

// Parked reports whether idle stop parked the session and its owner is still
// gone.
func (m *Manager) Parked(ctx context.Context, workspaceID, sessionKey string) bool {
	return m.StoppedMark(sessionKey) && m.PtyOwnerGone(ctx, workspaceID, sessionKey)
}

// Resumed clears the stop mark once the runtime runs again.
func (m *Manager) Resumed(sessionKey string) error {
	if m.parkedDir == "" {
		return nil
	}
	path, err := m.parkedPath(sessionKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// StoppedMark reports whether a session carries a stop mark, without asking
// its owner.
func (m *Manager) StoppedMark(sessionKey string) bool {
	path, err := m.parkedPath(sessionKey)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (m *Manager) markStopped(sessionKey string) error {
	path, err := m.parkedPath(sessionKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.parkedDir, 0o700); err != nil {
		return err
	}
	err = atomicfile.WriteFile(path, nil, atomicfile.WithPerm(0o600))
	if errors.Is(err, atomicfile.ErrPublished) {
		return nil
	}
	return err
}

// RetainParked removes the markers of sessions not in keep.
func (m *Manager) RetainParked(keep map[string]struct{}) error {
	if m.parkedDir == "" {
		return nil
	}
	entries, err := os.ReadDir(m.parkedDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	kept := make(map[string]bool, len(keep))
	for key := range keep {
		if path, err := m.parkedPath(key); err == nil {
			kept[filepath.Base(path)] = true
		}
	}
	var errs []error
	for _, entry := range entries {
		if !kept[entry.Name()] {
			if err := os.Remove(filepath.Join(m.parkedDir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) parkedPath(sessionKey string) (string, error) {
	if m.parkedDir == "" {
		return "", errors.New("parked runtime directory is required")
	}
	paths, err := ptyowner.NewSessionPaths(m.parkedDir, sessionKey)
	if err != nil {
		return "", fmt.Errorf("parked runtime marker: %w", err)
	}
	return paths.Dir, nil
}

// IdleActivity returns the last use per workspace that SaveIdleActivity saved.
func (m *Manager) IdleActivity() (map[string]time.Time, error) {
	active := make(map[string]time.Time)
	if m.parkedDir == "" {
		return active, nil
	}
	data, err := os.ReadFile(m.idleActivityPath())
	if errors.Is(err, os.ErrNotExist) {
		return active, nil
	}
	if err != nil {
		return active, err
	}
	return active, json.Unmarshal(data, &active)
}

// SaveIdleActivity replaces the saved last use per workspace, beside the stop
// marks, so a restart keeps idle time.
func (m *Manager) SaveIdleActivity(active map[string]time.Time) error {
	if m.parkedDir == "" {
		return nil
	}
	data, err := json.Marshal(active, json.Deterministic(true))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.idleActivityPath()), 0o700); err != nil {
		return err
	}
	err = atomicfile.WriteFile(m.idleActivityPath(), data, atomicfile.WithPerm(0o600))
	if errors.Is(err, atomicfile.ErrPublished) {
		return nil
	}
	return err
}

// ClearIdleState removes every stop mark and the saved last use per
// workspace, as when idle stop is turned off.
func (m *Manager) ClearIdleState() error {
	if m.parkedDir == "" {
		return nil
	}
	if err := os.RemoveAll(m.parkedDir); err != nil {
		return err
	}
	if err := os.Remove(m.idleActivityPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Manager) idleActivityPath() string {
	return filepath.Join(filepath.Dir(m.parkedDir), "idle-activity.json")
}

// PtyOwnerGone reports whether a stored pty-owner session has no live entry,
// did not exit while this manager watched it, and no owner answers for it.
func (m *Manager) PtyOwnerGone(ctx context.Context, workspaceID, sessionKey string) bool {
	if m.ptyOwnerRuntime == nil || m.Exited(sessionKey) {
		return false
	}
	if _, ok := m.session(workspaceID, sessionKey); ok {
		return false
	}
	return m.ptyOwnerRuntime.Gone(ctx, sessionKey)
}

// RestartShell starts a fresh plain shell under a parked shell's key.
func (m *Manager) RestartShell(ctx context.Context, restored RestoredRuntimeSession) (SessionInfo, error) {
	return m.launch(ctx, restored.WorkspaceID, restored.CWD, restored.TargetKey, &restored, "", "", "")
}
