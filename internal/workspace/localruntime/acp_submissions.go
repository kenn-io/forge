package localruntime

import (
	"errors"
	"slices"
	"time"
)

// acpTurnRecordLimit bounds the turn records kept in the session.
const acpTurnRecordLimit = 50

// ErrACPSubmissionUncertain rejects a submission an owner may have sent to an
// agent just before it crashed. Running it again could repeat its work. The
// owner RPC carries it as a code; attachments restore the sentinel.
var ErrACPSubmissionUncertain = errors.New("submission outcome is uncertain")

var errACPSubmissionWithdrawn = errors.New("submission was withdrawn")

// acpExitedDuringTurn ends a turn record that a replacement owner restored
// while it was still running.
const acpExitedDuringTurn = "agent process exited during the turn"

// ACPTurnRecord describes one prompt turn. A record without EndedAt is the
// running turn.
type ACPTurnRecord struct {
	SubmissionID string `json:"submissionId,omitempty"`
	StartedAt    string `json:"startedAt"`
	EndedAt      string `json:"endedAt,omitempty"`
	StopReason   string `json:"stopReason,omitempty"`
	Error        string `json:"error,omitempty"`
	ActiveMillis int64  `json:"activeMillis"`
	Allowance    int64  `json:"allowanceMillis,omitempty"`
	Exhausted    bool   `json:"allowanceExhausted,omitempty"`
}

// beginTurnRecordLocked records a turn that starts now, dropping the oldest
// records beyond the limit.
func (a *ACP) beginTurnRecordLocked(submissionID string, allowance int64) {
	a.state.Turns = append(a.state.Turns, ACPTurnRecord{
		SubmissionID: submissionID,
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
		Allowance:    allowance,
	})
	if excess := len(a.state.Turns) - acpTurnRecordLimit; excess > 0 {
		a.state.Turns = slices.Delete(a.state.Turns, 0, excess)
	}
}

// runningTurnRecordLocked returns the record of the running turn, or nil.
func (a *ACP) runningTurnRecordLocked() *ACPTurnRecord {
	if len(a.state.Turns) == 0 || a.state.Turns[len(a.state.Turns)-1].EndedAt != "" {
		return nil
	}
	return &a.state.Turns[len(a.state.Turns)-1]
}

// endTurnRecordLocked ends the running turn record, if there is one.
func (a *ACP) endTurnRecordLocked(stopReason string, err error) {
	record := a.runningTurnRecordLocked()
	if record == nil {
		return
	}
	record.EndedAt = time.Now().UTC().Format(time.RFC3339)
	record.StopReason = stopReason
	if err != nil {
		record.Error = err.Error()
	}
}

// restoreSubmissionsLocked carries submission outcomes into a replacement
// owner after the queue is restored. A prompt that was sending when the owner
// stopped may have reached the agent, so it leaves the queue it was sent
// from. A turn that was running did not finish in this process.
func (a *ACP) restoreSubmissionsLocked(saved ACPState) {
	a.state.Withdrawn = saved.Withdrawn
	a.state.Uncertain = saved.Uncertain
	if saved.Sending != nil {
		id := saved.Sending.ID
		if !slices.Contains(a.state.Uncertain, id) {
			a.state.Uncertain = append(a.state.Uncertain, id)
		}
		a.state.Queue = slices.DeleteFunc(a.state.Queue, func(queued ACPQueuedPrompt) bool { return queued.ID == id })
	}
	a.state.Turns = saved.Turns
	a.endTurnRecordLocked("", errors.New(acpExitedDuringTurn))
}

// dropUnrunnableQueueHeadLocked removes queued submissions at the head of
// the queue that must never run, so drain cannot send them.
func (a *ACP) dropUnrunnableQueueHeadLocked() {
	dropped := 0
	for dropped < len(a.state.Queue) && a.withdrawnOrUncertainLocked(a.state.Queue[dropped].ID) != nil {
		dropped++
	}
	if dropped == 0 {
		return
	}
	a.state.Queue = slices.Delete(a.state.Queue, 0, dropped)
	if len(a.state.Queue) == 0 {
		a.state.QueuePaused = false
	}
	if err := a.persistLocked(); err != nil {
		a.setErrorLocked(err)
	}
	a.changedLocked()
}

// withdrawnOrUncertainLocked rejects a submission ID that must never run.
func (a *ACP) withdrawnOrUncertainLocked(id string) error {
	if slices.Contains(a.state.Uncertain, id) {
		return ErrACPSubmissionUncertain
	}
	if slices.Contains(a.state.Withdrawn, id) {
		return errACPSubmissionWithdrawn
	}
	return nil
}
