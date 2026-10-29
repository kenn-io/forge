package localruntime

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"
)

// acpOwnerIdleTimeout is how long an owner keeps an idle chat's agent running
// once nobody uses or views the chat. Parking costs the next open one agent
// start, which reloads the saved session.
const acpOwnerIdleTimeout = 30 * time.Minute

// acpOwnerReapInterval bounds how often an owner checks for idleness and for
// a deleted state directory or socket.
const acpOwnerReapInterval = 30 * time.Second

// idleLocked reports whether parking the chat now loses nothing: no turn,
// steer, send, settings change, queued prompt, or pending question, and an
// agent that confirmed it can load the saved session back.
func (a *ACP) idleLocked() bool {
	return a.state.Connected && a.loadSession && a.recordPath != "" &&
		!a.state.Busy && !a.state.Stopping && !a.state.Configuring && !a.state.Steering &&
		a.state.Sending == nil && len(a.state.Queue) == 0 &&
		len(a.state.Permissions) == 0 && len(a.state.Elicitations) == 0 &&
		a.external == nil && !a.takeoverPending
}

// viewing records that a browser has the chat open until the returned func
// runs. The daemon's watch call carries it, so a daemon that goes away stops
// counting as a viewer.
func (a *ACP) viewing() func() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.viewers++
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.viewers--
		a.viewedAt = time.Now()
	}
}

// markParked records that the owner stops the agent to park the chat, not to
// end it.
func (a *ACP) markParked() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.parked = true
}

// Parked reports whether the owner parked the chat.
func (a *ACP) Parked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.parked
}

// parkIfIdle claims an idle chat for parking once nothing changed in it and
// nobody viewed it for timeout. It saves the session and disconnects the chat
// under turnMu, which every prompt starts under, so input that arrives later
// is refused unsent rather than accepted and lost.
func (a *ACP) parkIfIdle(now time.Time, timeout time.Duration) bool {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.idleLocked() || a.viewers > 0 || now.Sub(a.usedAt) < timeout || now.Sub(a.viewedAt) < timeout {
		return false
	}
	if err := a.persistLocked(); err != nil {
		slog.Warn("save idle ACP chat before parking", "err", err)
		return false
	}
	a.state.Connected = false
	a.parked = true
	a.changedLocked()
	return true
}

// reapACPOwner parks the chat once it has been idle and unviewed for timeout.
// It parks at once when no daemon can reach this owner again: its state
// directory is gone, or its socket path no longer names this listener, as
// when a test daemon removes its data directory while its tmux server lives.
func reapACPOwner(ctx context.Context, agent *ACP, service *acpOwnerRPC, dir, socket string, timeout time.Duration) {
	listening, err := os.Stat(socket)
	if err != nil {
		listening = nil
	}
	ticker := time.NewTicker(min(timeout/4, acpOwnerReapInterval))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-agent.Done():
			return
		case <-service.stop:
			return
		case now := <-ticker.C:
			if acpOwnerUnreachable(dir, socket, listening) {
				slog.Warn("ACP owner state was removed; parking the chat", "dir", dir)
				_ = service.shutdown(true)
				return
			}
			if agent.parkIfIdle(now, timeout) {
				slog.Info("parking idle ACP chat", "dir", dir, "idle", timeout)
				_ = service.shutdown(true)
				return
			}
		}
	}
}

func acpOwnerUnreachable(dir, socket string, listening os.FileInfo) bool {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if listening == nil {
		return false
	}
	current, err := os.Stat(socket)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	return err == nil && !os.SameFile(listening, current)
}
