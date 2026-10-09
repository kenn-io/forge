package localruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

func (a *ACP) clockNow() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// blockedLocked reports whether the running turn waits only for a person: a
// permission or elicitation is pending and no tool call of the turn is
// running. A tool call a pending permission asks to run waits on that
// permission, so it does not count as running.
func (a *ACP) blockedLocked() bool {
	if !a.state.Busy || len(a.state.Permissions)+len(a.state.Elicitations) == 0 {
		return false
	}
	for _, message := range a.state.Messages[min(a.turnStart, len(a.state.Messages)):] {
		running := message.Status == string(acpsdk.ToolCallStatusPending) || message.Status == string(acpsdk.ToolCallStatusInProgress)
		if message.Role != "tool" || !running {
			continue
		}
		asked := slices.ContainsFunc(a.state.Permissions, func(p ACPPermission) bool { return p.toolCallID == message.ToolCallID })
		if !asked {
			return false
		}
	}
	return true
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
		a.activeSince = a.clockNow()
	}
}

// accrueLocked adds the active time since the last accrual to the running
// turn record.
func (a *ACP) accrueLocked() {
	record := a.runningTurnRecordLocked()
	if record == nil || a.activeSince.IsZero() {
		return
	}
	elapsed := a.clockNow().Sub(a.activeSince).Milliseconds()
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
	a.turnStart = firstMessage
	stop := make(chan struct{})
	a.stopClock = stop
	a.clockSaved = a.clockNow()
	a.refreshBlockedLocked()
	tick := a.allowanceTick
	if tick <= 0 {
		tick = acpTurnClockTick
	}
	go a.runTurnClock(stop, tick)
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

func (a *ACP) runTurnClock(stop <-chan struct{}, tick time.Duration) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if a.tickTurnClock(stop) {
				a.cancelExhaustedTurn()
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
	exhausted := record.Allowance > 0 && !record.Exhausted && record.ActiveMillis >= record.Allowance
	now := a.clockNow()
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
// out. The turn then ends through finishTurn like any stopped turn.
func (a *ACP) cancelExhaustedTurn() {
	if err := a.sendCancel(); err != nil {
		a.mu.Lock()
		a.setErrorLocked(fmt.Errorf("cancel a turn that used its allowance: %w", err))
		a.changedLocked()
		a.mu.Unlock()
	}
}

// cancel stops the running turn. It never waits for a prompt write, steering
// request, or settings change; a prompt written concurrently is cancelled
// after its write.
func (a *ACP) cancel() error {
	a.mu.Lock()
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
