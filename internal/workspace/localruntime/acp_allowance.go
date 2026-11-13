package localruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// acpTurnClockTick is how often a running turn's clock counts active time
// and checks the turn's allowance.
const acpTurnClockTick = time.Second

// acpTurnClockSaveInterval bounds how often the turn clock saves the session.
const acpTurnClockSaveInterval = 10 * time.Second

var (
	errACPNegativeAllowance     = errors.New("allowance must be positive")
	errACPUnsupervisedAllowance = errors.New("only a supervisor's prompts may set an allowance")
)

// blockedLocked reports whether the running turn waits only for a person: a
// permission or elicitation is pending and no tool call of the turn is
// running. A tool call a pending permission asks to run waits on that
// permission, and so do the subagent tool calls it runs under, so none of
// them counts as running. An elicitation names no tool call, so every running
// tool call counts while one is pending.
func (a *ACP) blockedLocked() bool {
	if !a.state.Busy || len(a.state.Permissions)+len(a.state.Elicitations) == 0 {
		return false
	}
	turn := a.state.Messages[min(a.turnStart, len(a.state.Messages)):]
	waiting := waitingToolCalls(turn, a.state.Permissions)
	for _, message := range turn {
		running := message.Status == string(acpsdk.ToolCallStatusPending) || message.Status == string(acpsdk.ToolCallStatusInProgress)
		if message.Role == "tool" && running && !waiting[message.ToolCallID] {
			return false
		}
	}
	return true
}

// waitingToolCalls returns the tool calls pending permissions ask to run,
// with every tool call each one runs under.
func waitingToolCalls(turn []ACPMessage, permissions []ACPPermission) map[string]bool {
	parents := map[string]string{}
	for _, message := range turn {
		if message.Role == "tool" && message.ParentToolCallID != "" {
			parents[message.ToolCallID] = message.ParentToolCallID
		}
	}
	waiting := map[string]bool{}
	for _, permission := range permissions {
		// The seen check also ends a cycle in agent-reported lineage.
		for id := permission.toolCallID; id != "" && !waiting[id]; id = parents[id] {
			waiting[id] = true
		}
	}
	return waiting
}

// refreshBlockedLocked recomputes Blocked. Callers run it whenever the turn,
// its requests, or its tool call statuses change, so the clock counts only
// time the turn was not blocked.
func (a *ACP) refreshBlockedLocked() {
	a.accrueLocked()
	a.state.Blocked = a.blockedLocked()
	counting := a.stopClock != nil && !a.state.Blocked
	if !counting {
		a.activeSince = time.Time{}
	} else if a.activeSince.IsZero() {
		a.activeSince = time.Now()
	}
}

// accrueLocked adds the active time since the last accrual to the running
// turn record.
func (a *ACP) accrueLocked() {
	record := a.runningTurnRecordLocked()
	if record == nil || a.activeSince.IsZero() {
		return
	}
	elapsed := time.Since(a.activeSince).Milliseconds()
	if elapsed <= 0 {
		return
	}
	record.ActiveMillis += elapsed
	a.activeSince = a.activeSince.Add(time.Duration(elapsed) * time.Millisecond)
}

// startTurnClockLocked starts counting the active time of a turn that begins
// now. firstMessage is the transcript index of the turn's first message.
func (a *ACP) startTurnClockLocked(firstMessage int) {
	if a.stopClock != nil {
		// Every turn ends before another begins; never leave a clock behind.
		close(a.stopClock)
	}
	// Time counted before this turn began belongs to no turn.
	a.activeSince = time.Time{}
	a.turnStart = firstMessage
	stop := make(chan struct{})
	a.stopClock = stop
	a.clockSaved = time.Now()
	a.refreshBlockedLocked()
	go a.runTurnClock(stop)
}

// stopTurnClockLocked counts the turn's last active time and stops its clock.
// Callers save the session as part of ending the turn.
func (a *ACP) stopTurnClockLocked() {
	if a.stopClock == nil {
		return
	}
	a.accrueLocked()
	close(a.stopClock)
	a.stopClock = nil
	a.refreshBlockedLocked()
}

func (a *ACP) runTurnClock(stop <-chan struct{}) {
	ticker := time.NewTicker(acpTurnClockTick)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-a.done:
			return
		case <-ticker.C:
			if a.tickTurnClock(stop) {
				if a.beforeAllowanceCancel != nil {
					a.beforeAllowanceCancel()
				}
				a.cancelExhaustedTurn(stop)
			}
		}
	}
}

// tickTurnClock counts active time and reports whether the turn has just
// used up its allowance, in which case it is already stopping.
func (a *ACP) tickTurnClock(stop <-chan struct{}) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopClock != stop {
		// The turn ended after this tick fired.
		return false
	}
	a.accrueLocked()
	record := a.runningTurnRecordLocked()
	if record == nil {
		return false
	}
	// A turn someone already stopped needs no second cancel.
	exhausted := record.Allowance > 0 && !record.Exhausted && !a.cancelling && record.ActiveMillis >= record.Allowance
	now := time.Now()
	if !exhausted && now.Sub(a.clockSaved) < acpTurnClockSaveInterval {
		return false
	}
	if exhausted {
		record.Exhausted = true
		a.stopTurnLocked()
	}
	a.clockSaved = now
	if err := a.persistLocked(); err != nil {
		a.setErrorLocked(err)
	}
	a.changedLocked()
	return exhausted
}

// cancelExhaustedTurn sends the cancel that ends a turn whose allowance ran
// out; stop is that turn's clock. The turn then ends through finishTurn like
// any stopped turn. ACP cancels whatever turn the session is running, so the
// cancel is sent only while the exhausted turn still runs, and under turnMu,
// which every prompt that starts a turn holds: another turn cannot start
// between the check and the cancel.
func (a *ACP) cancelExhaustedTurn(stop <-chan struct{}) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.mu.Lock()
	running := a.stopClock == stop
	a.mu.Unlock()
	if !running {
		// The turn ended before its cancel went out.
		return
	}
	if err := a.sendCancel(); err != nil {
		a.mu.Lock()
		a.setErrorLocked(fmt.Errorf("cancel a turn that used its allowance: %w", err))
		a.changedLocked()
		a.mu.Unlock()
	}
}

// cancel stops the running turn. A person can always stop it, and never
// waits for a prompt write, steering request, or settings change; a prompt
// written concurrently is cancelled after its write. A coordinator names the
// generation it holds and may stop only a turn its own prompt started. Its
// check and send hold turnMu, as an exhausted allowance's do, so its cancel
// never reaches a turn someone else started after its own ended.
func (a *ACP) cancel(generation uint64) error {
	if generation != 0 {
		a.turnMu.Lock()
		defer a.turnMu.Unlock()
	}
	a.mu.Lock()
	if err := a.turnGateLocked(generation); err != nil {
		idle := a.runningTurnRecordLocked() == nil
		a.mu.Unlock()
		if errors.Is(err, ErrACPNotOwned) && idle {
			// Its turn already ended; a retried cancel has nothing to stop.
			return nil
		}
		return err
	}
	a.stopTurnLocked()
	a.changedLocked()
	a.mu.Unlock()
	return a.sendCancel()
}

// stopTurnLocked records a stop request: queued work waits, and open
// questions are answered as cancelled.
func (a *ACP) stopTurnLocked() {
	a.cancelling = true
	a.state.QueuePaused = true
	a.state.Stopping = a.state.Busy || a.state.Steering
	a.clearPendingLocked()
}

func (a *ACP) sendCancel() error {
	return a.client.Cancel(context.Background(), acpsdk.CancelNotification{SessionId: acpsdk.SessionId(a.sessionID)})
}
