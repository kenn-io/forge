package localruntime

import (
	"errors"
	"strings"
	"time"
)

// Supervision failures. The owner RPC carries each as a code; attachments
// restore the sentinel.
var (
	// ErrACPBusy rejects a supervisor's prompt while a turn, a steering
	// request, or a settings change runs: supervised prompts never queue.
	ErrACPBusy = errors.New("ACP agent is running a turn")
	// ErrACPSupervised rejects input that carries no supervision generation
	// while a coordinator supervises the chat.
	ErrACPSupervised = errors.New("this chat is supervised by another client; take over to send input")
	// ErrACPStaleGeneration rejects input or a claim made under an earlier
	// supervision generation.
	ErrACPStaleGeneration = errors.New("supervision generation is stale")
	// ErrACPQueuePending refuses a claim while earlier input is queued, so
	// that input cannot run under the coordinator's supervision.
	ErrACPQueuePending = errors.New("unqueue pending messages before supervising")
)

// ACPSupervision records the coordinator that supervises a chat. Every claim
// and takeover advances Generation, so input sent under an earlier one is
// stale. After a takeover, input without a generation is accepted again.
type ACPSupervision struct {
	Supervisor string `json:"supervisor"`
	Generation uint64 `json:"generation"`
	TakenOver  bool   `json:"takenOver"`
	ChangedAt  string `json:"changedAt"`
}

// supervise claims the chat for command.Supervisor. The claim carries the
// generation the caller last read, 0 for an unsupervised chat, so a delayed
// claim cannot replace a newer claim or a takeover.
func (a *ACP) supervise(command ACPCommand) error {
	if strings.TrimSpace(command.Supervisor) == "" {
		return errors.New("supervisor is required")
	}
	// Submissions, steers, and drains hold turnMu while they decide to queue
	// or start input, so none of them can queue input after this check.
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	var current uint64
	if a.state.Supervision != nil {
		current = a.state.Supervision.Generation
	}
	if command.Generation != current {
		return ErrACPStaleGeneration
	}
	if len(a.state.Queue) > 0 {
		return ErrACPQueuePending
	}
	return a.setSupervisionLocked(ACPSupervision{Supervisor: command.Supervisor, Generation: current + 1})
}

// takeover returns a supervised chat to people. A running turn becomes
// theirs, so the supervisor's allowance no longer bounds it. Repeating it, or
// taking over a chat nobody supervises, changes nothing.
func (a *ACP) takeover() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.supervisedLocked() {
		return nil
	}
	next := *a.state.Supervision
	next.TakenOver = true
	next.Generation++
	record := a.runningTurnRecordLocked()
	var allowance int64
	if record != nil {
		allowance, record.Allowance = record.Allowance, 0
	}
	err := a.setSupervisionLocked(next)
	if err != nil && record != nil {
		record.Allowance = allowance
	}
	return err
}

// setSupervisionLocked saves a new supervision record. A record that cannot
// be saved does not take effect.
func (a *ACP) setSupervisionLocked(next ACPSupervision) error {
	previous := a.state.Supervision
	next.ChangedAt = time.Now().UTC().Format(time.RFC3339)
	a.state.Supervision = &next
	if err := a.persistLocked(); err != nil {
		a.state.Supervision = previous
		return err
	}
	a.changedLocked()
	return nil
}

// supervisedLocked reports whether a coordinator holds the chat.
func (a *ACP) supervisedLocked() bool {
	return a.state.Supervision != nil && !a.state.Supervision.TakenOver
}

// supervisionGateLocked rejects input sent under another supervision
// generation, and input without a generation until a person takes over.
func (a *ACP) supervisionGateLocked(generation uint64) error {
	supervision := a.state.Supervision
	if supervision == nil {
		return nil
	}
	if generation != 0 && generation != supervision.Generation {
		return ErrACPStaleGeneration
	}
	if generation == 0 && !supervision.TakenOver {
		return ErrACPSupervised
	}
	return nil
}

// supervisedPromptLocked applies the gate to a prompt and reports whether it
// is the coordinator's. The coordinator's prompts start a turn at once, so
// they must be identified sends.
func (a *ACP) supervisedPromptLocked(command ACPCommand) (bool, error) {
	if err := a.supervisionGateLocked(command.Generation); err != nil {
		return false, err
	}
	if !a.supervisedLocked() {
		return false, nil
	}
	if command.Mode != "" && command.Mode != "send" {
		return true, errors.New("supervised prompts must be sent, not queued or steered")
	}
	if command.ID == "" {
		return true, errors.New("supervised prompts require a submission ID")
	}
	return true, nil
}
