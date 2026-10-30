package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/forge/internal/tracing"
	"go.kenn.io/kit/atomicfile"
)

type acpOwnerMismatchError struct {
	protocol int
	state    []byte
}

func (e *acpOwnerMismatchError) Error() string {
	return fmt.Sprintf("ACP owner protocol %d is incompatible with daemon protocol %d", e.protocol, ACPOwnerProtocol)
}

// Replacement runs before attachment, so the old owner's exit cannot remove
// the persisted runtime identity. Park retains the native session and transcript.
// A turn waiting on a person's answer can never get one through this daemon, so
// it ends with the old agent; any other running work finishes first.
func (m *Manager) replaceACPOwner(ctx context.Context, info SessionInfo, mismatch *acpOwnerMismatchError) (err error) {
	ctx, span := acpTracer.Start(ctx, "acp.owner.replace", acpSpanAttributes(info))
	defer func() { tracing.EndSpan(span, err) }()
	if mismatch.protocol > ACPOwnerProtocol {
		return errors.New("owner is newer than this daemon; update Forge before reopening the chat")
	}
	var state ACPState
	if err := json.Unmarshal(mismatch.state, &state); err != nil {
		return fmt.Errorf("decode owner state before recovery: %w", err)
	}
	waitingOnPerson := len(state.Permissions) != 0 || len(state.Elicitations) != 0
	if !waitingOnPerson && (state.Busy || state.Steering || state.Configuring) {
		return errors.New("chat has an active operation; it reloads after that operation finishes")
	}
	if !m.ACPReloadable(info.Key) {
		return errors.New("saved chat does not confirm native session resume support; owner was left running")
	}
	if err := m.saveACPOwnerUpgrade(info.Key, state); err != nil {
		return fmt.Errorf("save queued prompts and unanswered questions for the new owner: %w", err)
	}
	slog.Info("replacing incompatible ACP owner", "workspace_id", info.WorkspaceID, "session_key", info.Key, "owner_protocol", mismatch.protocol, "daemon_protocol", ACPOwnerProtocol)
	if err := m.callACPOwner(ctx, info.Key, "ACP.Park"); err != nil {
		return fmt.Errorf("park old owner: %w", err)
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

// acpOwnerUpgradeFile holds what a replaced owner hands to its replacement. It
// is written before the old owner parks and removed once a new owner applies
// it, so a failed reload keeps it for the next attempt.
const acpOwnerUpgradeFile = "upgrade.json"

// acpOwnerUpgrade is the part of an old owner's state its replacement cannot
// take from the saved session: Queue is the prompts it published, including
// any it failed to save, and Cancelled names the requests that end with it.
type acpOwnerUpgrade struct {
	Queue     []ACPQueuedPrompt
	Cancelled []string
}

// saveACPOwnerUpgrade records state's handoff, replacing any left by an earlier
// attempt. A chat with nothing to hand over keeps no file.
func (m *Manager) saveACPOwnerUpgrade(key string, state ACPState) error {
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, key)
	if err != nil {
		return err
	}
	path := filepath.Join(paths.Dir, acpOwnerUpgradeFile)
	upgrade := acpOwnerUpgrade{Queue: slices.Concat(state.Steers, state.Queue)}
	for _, permission := range state.Permissions {
		name := permission.Title
		if name == "" {
			name = "Agent requests permission"
		}
		upgrade.Cancelled = append(upgrade.Cancelled, name)
	}
	for _, elicitation := range state.Elicitations {
		upgrade.Cancelled = append(upgrade.Cancelled, elicitation.Message)
	}
	if len(upgrade.Queue) == 0 && len(upgrade.Cancelled) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(upgrade)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(path, data, atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return err
	}
	return nil
}

// applyOwnerUpgrade takes over what a replaced owner left in dir: prompts the
// saved session lacks join the queue behind the ones it restored, the queue
// runs again, and one notice names the requests that ended with the old agent.
// Saved or already-run submissions are the same prompt, so none runs twice.
func (a *ACP) applyOwnerUpgrade(dir string) {
	path := filepath.Join(dir, acpOwnerUpgradeFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var upgrade acpOwnerUpgrade
	if err == nil {
		err = json.Unmarshal(data, &upgrade)
	}
	if err == nil {
		err = os.Remove(path)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.setErrorLocked(fmt.Errorf("apply the Forge upgrade to this chat: %w", err))
		a.changedLocked()
		return
	}
	for _, prompt := range upgrade.Queue {
		submitted, err := a.submittedLocked(prompt.ID, prompt.Text, prompt.Images)
		if err != nil && !errors.Is(err, errACPSubmissionWithdrawn) {
			a.setErrorLocked(fmt.Errorf("restore queued prompt: %w", err))
		}
		if !submitted {
			a.enqueueLocked(prompt, false)
		}
	}
	if notice := acpCancelledNotice(upgrade.Cancelled); notice != "" {
		a.state.Notices = append(a.state.Notices, notice)
	}
	a.state.QueuePaused = false
	a.changedLocked()
	go a.drain()
}

func acpCancelledNotice(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = strconv.Quote(name)
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return "The Forge upgrade cancelled an unanswered question: " + quoted[0]
	default:
		return "The Forge upgrade cancelled unanswered questions: " + strings.Join(quoted, ", ")
	}
}
