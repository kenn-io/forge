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
		return errors.New("this chat is running under a newer Forge version; update Forge to open it")
	}
	var state ACPState
	if err := json.Unmarshal(mismatch.state, &state); err != nil {
		return fmt.Errorf("read this chat from the previous Forge version: %w", err)
	}
	waitingOnPerson := len(state.Permissions) != 0 || len(state.Elicitations) != 0
	if !waitingOnPerson && (state.Busy || state.Steering || state.Configuring) {
		return errors.New("this chat is still working under the previous Forge version; it reloads when that work finishes")
	}
	if !m.ACPReloadable(info.Key) {
		return errors.New("this chat's agent cannot reload its session, so it stays on the previous Forge version; closing its tab stops the agent and deletes the chat")
	}
	if err := m.saveACPOwnerUpgrade(info.Key, state); err != nil {
		return fmt.Errorf("save queued prompts and unanswered questions for the new owner: %w", err)
	}
	slog.Info("replacing incompatible ACP owner", "workspace_id", info.WorkspaceID, "session_key", info.Key, "owner_protocol", mismatch.protocol, "daemon_protocol", ACPOwnerProtocol)
	if err := m.callACPOwner(ctx, info.Key, "ACP.Park"); err != nil {
		return fmt.Errorf("stop the agent from the previous Forge version: %w", err)
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
			return fmt.Errorf("wait for the previous agent to stop: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// acpOwnerUpgradeFile holds what a replaced owner hands to its replacement. It
// is written before the old owner parks and removed once a new owner saves what
// it holds, so a failed reload or save keeps it for the next attempt.
const acpOwnerUpgradeFile = "upgrade.json"

// acpOwnerUpgrade is the part of an old owner's state its replacement cannot
// take from the saved session: Queue is the prompts it published in the order
// it published them, including any it failed to save, Withdrawn names the
// prompts a person removed, including removals it failed to save, and
// Cancelled names the requests that end with it.
type acpOwnerUpgrade struct {
	Queue     []ACPQueuedPrompt
	Withdrawn []string
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
	upgrade := acpOwnerUpgrade{Queue: slices.Concat(state.Steers, state.Queue), Withdrawn: state.Withdrawn}
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
	if len(upgrade.Queue) == 0 && len(upgrade.Withdrawn) == 0 && len(upgrade.Cancelled) == 0 {
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

// applyOwnerUpgrade takes over what a replaced owner left in dir: its prompts
// run in the order it published them, behind only the saved prompts it did not
// publish, the queue runs again, and one notice names the requests that ended
// with the old agent. A saved prompt is the same prompt as the published one
// with its submission, and a withdrawn or already-run one does not run, so
// none runs twice, including when a kept handoff applies again. A chat it
// cannot save after taking the handoff over returns the error and must not
// run: the kept handoff would apply again over its later changes.
func (a *ACP) applyOwnerUpgrade(dir string) error {
	path := filepath.Join(dir, acpOwnerUpgradeFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var upgrade acpOwnerUpgrade
	if err == nil {
		err = json.Unmarshal(data, &upgrade)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.setErrorLocked(fmt.Errorf("apply the Forge upgrade to this chat: %w", err))
		a.changedLocked()
		return nil
	}
	for _, id := range upgrade.Withdrawn {
		if !slices.Contains(a.state.Withdrawn, id) {
			a.state.Withdrawn = append(a.state.Withdrawn, id)
		}
	}
	a.state.Queue = slices.DeleteFunc(a.state.Queue, func(saved ACPQueuedPrompt) bool {
		return slices.Contains(a.state.Withdrawn, saved.ID) ||
			slices.ContainsFunc(upgrade.Queue, func(published ACPQueuedPrompt) bool { return published.ID == saved.ID })
	})
	for _, prompt := range upgrade.Queue {
		submitted, err := a.submittedLocked(prompt.ID, prompt.Text, prompt.Images)
		if err != nil && !errors.Is(err, errACPSubmissionWithdrawn) {
			a.setErrorLocked(fmt.Errorf("restore queued prompt: %w", err))
		}
		if !submitted {
			a.enqueueLocked(prompt, false)
		}
	}
	// An owner from before senders were recorded publishes prompts without one.
	a.restoreSendersLocked()
	if notice := acpCancelledNotice(upgrade.Cancelled); notice != "" {
		a.state.Notices = append(a.state.Notices, notice)
	}
	a.state.QueuePaused = false
	if err := a.persistLocked(); err != nil {
		return fmt.Errorf("save this chat after the Forge upgrade: %w", err)
	}
	if err := os.Remove(path); err != nil {
		slog.Warn("remove ACP owner upgrade handoff", "path", path, "err", err)
	}
	a.changedLocked()
	go a.drain()
	return nil
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
