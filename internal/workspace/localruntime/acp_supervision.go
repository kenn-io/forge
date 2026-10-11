package localruntime

import (
	"errors"
	"strings"
	"time"
)

// Supervision failures. The owner RPC carries each as a code; attachments
// restore the sentinel.
var (
	// ErrACPSupervised rejects input an agent sends through a Forge tool
	// while a coordinator supervises the chat. People's input is never
	// refused for supervision.
	ErrACPSupervised = errors.New("this chat is supervised by a coordinator")
	// ErrACPStaleGeneration rejects input or a claim made under an earlier
	// supervision generation.
	ErrACPStaleGeneration = errors.New("supervision generation is stale")
	// ErrACPNotOwned refuses a coordinator's command on a queued prompt, turn,
	// or request that belongs to someone else.
	ErrACPNotOwned = errors.New("this belongs to someone else")
	// ErrACPPersonOnly refuses a coordinator a command only people may give:
	// steering, reordering the queue, and resuming it.
	ErrACPPersonOnly = errors.New("only a person can do this")
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
// claim cannot replace a newer claim or a takeover. A claim may come at any
// time: earlier input keeps its sender, so it never runs as the
// coordinator's.
func (a *ACP) supervise(command ACPCommand) error {
	if strings.TrimSpace(command.Supervisor) == "" {
		return errors.New("supervisor is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var current uint64
	if a.state.Supervision != nil {
		current = a.state.Supervision.Generation
	}
	if command.Generation != current {
		return ErrACPStaleGeneration
	}
	return a.setSupervisionLocked(ACPSupervision{Supervisor: command.Supervisor, Generation: current + 1})
}

// takeover ends supervision. The coordinator can send nothing more, and its
// running turn is no longer bounded by its allowance; the turn and its
// queued prompts stay. People never need it to chat, and may end supervision at any
// time. A coordinator releasing its claim names the generation it holds, so
// a delayed release never ends a later claim. Repeating it, or taking over a
// chat nobody supervises, changes nothing.
func (a *ACP) takeover(generation uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.supervisionGateLocked(generation); err != nil {
		return err
	}
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

// setSupervisionLocked saves a new supervision record. Queued prompts are
// not touched: a coordinator's prompt accepted into the queue stays an
// ordinary queued prompt whoever holds the chat later. A record that cannot
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

// held reports whether a coordinator holds a chat with this record. A chat
// people took over is theirs, though it keeps the record.
func (s *ACPSupervision) held() bool {
	return s != nil && !s.TakenOver
}

// supervisedLocked reports whether a coordinator holds the chat.
func (a *ACP) supervisedLocked() bool {
	return a.state.Supervision.held()
}

// heldAtLocked reports whether a coordinator still holds the chat at generation.
func (a *ACP) heldAtLocked(generation uint64) bool {
	return a.supervisedLocked() && a.state.Supervision.Generation == generation
}

// supervisionGateLocked rejects input sent under a generation the sender no
// longer holds. Input without a generation is a person's, or an agent's that
// submit checks separately. After a takeover every generation is stale: no
// coordinator holds the chat, so a coordinator's input never passes.
func (a *ACP) supervisionGateLocked(generation uint64) error {
	if generation != 0 && !a.heldAtLocked(generation) {
		return ErrACPStaleGeneration
	}
	return nil
}

// ownsRunningTurnLocked reports whether the coordinator holding generation
// started the running turn.
func (a *ACP) ownsRunningTurnLocked(generation uint64) bool {
	record := a.runningTurnRecordLocked()
	return record != nil && record.Sender.coordinator() && record.Generation == generation
}

// turnGateLocked applies the gate to a command on the running turn or its
// requests: a coordinator may act only on a turn its own prompt started.
func (a *ACP) turnGateLocked(generation uint64) error {
	if err := a.supervisionGateLocked(generation); err != nil {
		return err
	}
	if generation != 0 && !a.ownsRunningTurnLocked(generation) {
		return ErrACPNotOwned
	}
	return nil
}

// personGate refuses coordinators a command only people may give.
func personGate(generation uint64) error {
	if generation != 0 {
		return ErrACPPersonOnly
	}
	return nil
}

// promptSenderLocked applies the gate to a prompt and returns its sender.
// A coordinator's prompts must be identified so it can settle their outcomes,
// and may not steer. An agent that does not hold a supervised chat cannot
// add to it.
func (a *ACP) promptSenderLocked(command ACPCommand) (ACPSender, error) {
	if err := a.supervisionGateLocked(command.Generation); err != nil {
		return ACPSender{}, err
	}
	switch {
	case command.Generation != 0:
		if command.Mode == "steer" {
			return ACPSender{}, ErrACPPersonOnly
		}
		if command.ID == "" {
			return ACPSender{}, errors.New("a coordinator's prompts require a submission ID")
		}
		return ACPSender{Kind: ACPSenderCoordinator, Name: a.state.Supervision.Supervisor}, nil
	case command.FromAgent:
		if a.supervisedLocked() {
			return ACPSender{}, ErrACPSupervised
		}
		return ACPSender{Kind: ACPSenderCoordinator}, nil
	default:
		return ACPSender{Kind: ACPSenderUser}, nil
	}
}
