package localruntime

import (
	"errors"
	"slices"
	"time"
)

// acpTurnRecordLimit bounds the turn records kept in the session.
const acpTurnRecordLimit = 50

var errACPSubmissionWithdrawn = errors.New("submission was withdrawn")

// acpExitedDuringTurn ends a turn record that a replacement owner restored
// while it was still running.
const acpExitedDuringTurn = "agent process exited during the turn"

// ACPTurnRecord describes one prompt turn. A record without EndedAt is the
// running turn.
type ACPTurnRecord struct {
	SubmissionID string    `json:"submissionId,omitempty"`
	Sender       ACPSender `json:"sender"`
	// Generation is the supervision generation of the coordinator whose
	// prompt started the turn, or zero for anyone else's.
	Generation   uint64 `json:"generation,omitempty"`
	StartedAt    string `json:"startedAt"`
	EndedAt      string `json:"endedAt,omitempty"`
	StopReason   string `json:"stopReason,omitempty"`
	Error        string `json:"error,omitempty"`
	ActiveMillis int64  `json:"activeMillis"`
	Allowance    int64  `json:"allowanceMillis,omitempty"`
	Exhausted    bool   `json:"allowanceExhausted,omitempty"`
	// FirstMessage is the transcript index of the turn's first message. The turn's messages run
	// up to the next turn's first message, so a coordinator can tell a person's steering, which
	// joins the running turn, from a new turn. Records saved before Forge kept it leave it nil.
	FirstMessage *int `json:"firstMessage,omitempty"`
}

// beginTurnRecordLocked records a turn prompt starts now, dropping the oldest
// records beyond the limit, and starts its clock. firstMessage is the
// transcript index of the turn's first message.
func (a *ACP) beginTurnRecordLocked(prompt ACPQueuedPrompt, firstMessage int) {
	first := firstMessage
	a.state.Turns = append(a.state.Turns, ACPTurnRecord{
		SubmissionID: prompt.ID,
		Sender:       prompt.Sender,
		Generation:   prompt.Generation,
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
		Allowance:    prompt.Allowance,
		FirstMessage: &first,
	})
	if excess := len(a.state.Turns) - acpTurnRecordLimit; excess > 0 {
		a.state.Turns = slices.Delete(a.state.Turns, 0, excess)
	}
	a.startTurnClockLocked(firstMessage)
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
	a.stopTurnClockLocked()
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
// owner after the queue is restored. Every accepted prompt still runs: a
// prompt that was sending when the owner stopped stays at the head of the
// queue, or goes back to it (the owner records its message and clears
// sending in one save), and steers the agent had not taken lead the queue. A turn that was running did not finish
// in this process.
func (a *ACP) restoreSubmissionsLocked(saved ACPState) {
	a.state.Withdrawn = saved.Withdrawn
	var restored []ACPQueuedPrompt
	if sending := saved.Sending; sending != nil &&
		!slices.ContainsFunc(a.state.Queue, func(queued ACPQueuedPrompt) bool { return queued.ID == sending.ID }) {
		restored = append(restored, *sending)
	}
	restored = append(restored, saved.Steers...)
	a.state.Queue = slices.Insert(a.state.Queue, 0, restored...)
	a.state.Turns = saved.Turns
	a.endTurnRecordLocked("", errors.New(acpExitedDuringTurn))
}

// dropUnrunnableQueueHeadLocked removes withdrawn submissions at the head of
// the queue, so drain cannot send them.
func (a *ACP) dropUnrunnableQueueHeadLocked() {
	dropped := 0
	for dropped < len(a.state.Queue) && slices.Contains(a.state.Withdrawn, a.state.Queue[dropped].ID) {
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
