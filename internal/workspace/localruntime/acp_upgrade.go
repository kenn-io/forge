package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type acpOwnerMismatchError struct {
	protocol int
	state    []byte
}

func (e *acpOwnerMismatchError) Error() string {
	return fmt.Sprintf("ACP owner protocol %d is incompatible with daemon protocol %d", e.protocol, acpOwnerProtocol)
}

// Replacement runs before attachment, so the old owner's exit cannot remove
// the persisted runtime identity. Park retains the native session and transcript.
func (m *Manager) replaceACPOwner(ctx context.Context, info SessionInfo, mismatch *acpOwnerMismatchError) error {
	if mismatch.protocol > acpOwnerProtocol {
		return errors.New("owner is newer than this daemon; update Forge before reopening the chat")
	}
	var state ACPState
	if err := json.Unmarshal(mismatch.state, &state); err != nil {
		return fmt.Errorf("decode owner state before recovery: %w", err)
	}
	if state.Busy || state.Steering || len(state.Permissions) != 0 || len(state.Elicitations) != 0 || len(state.Queue) != 0 {
		return errors.New("chat has active or pending work; reopen it after that work finishes")
	}
	if !m.ACPReloadable(info.Key) {
		return errors.New("saved chat does not confirm native session resume support; owner was left running")
	}
	slog.Info("replacing incompatible ACP owner", "workspace_id", info.WorkspaceID, "session_key", info.Key, "owner_protocol", mismatch.protocol, "daemon_protocol", acpOwnerProtocol)
	if err := m.callACPOwner(ctx, info.Key, "ACP.Park"); err != nil {
		return fmt.Errorf("preserve and park old owner: %w", err)
	}
	// Park closes the RPC socket before acknowledging, but tmux/ptyowner still
	// needs to reap the process. Do not start a second owner while it is alive.
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if info.TmuxSession != "" {
			err := m.requireTmuxSession(waitCtx, info.TmuxSession)
			if errors.Is(err, ErrSessionNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
		} else if m.PtyOwnerGone(waitCtx, info.WorkspaceID, info.Key) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for parked ACP owner: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}
