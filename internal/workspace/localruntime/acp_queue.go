package localruntime

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// acpSteeringMethod is the agent extension that adds user input to a running
// turn. Agents advertise it with initialize _meta.steering.supported.
const acpSteeringMethod = "_session/steering"

const acpSteeringTimeout = 30 * time.Second

type acpTurnResult struct {
	stopReason acpsdk.StopReason
	err        error
}

// submit never rejects input because a turn is running: a busy chat queues
// the prompt, or steers it into the turn when asked and supported.
func (a *ACP) submit(command ACPCommand) error {
	text := command.Text
	if strings.TrimSpace(text) == "" || len(text) > 64<<10 {
		return errors.New("message must contain between 1 and 65536 bytes")
	}
	switch command.Mode {
	case "", "send", "queue", "steer":
	default:
		return errors.New("prompt mode must be send, queue, or steer")
	}
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.mu.Lock()
	if submitted, err := a.submittedLocked(command.ID, text); submitted || err != nil {
		a.mu.Unlock()
		return err
	}
	if !a.state.Connected {
		a.mu.Unlock()
		return ErrACPAgentUnavailable
	}
	running := a.state.Busy || a.state.Steering
	if !running && len(a.state.Queue) == 0 {
		// A new message after a stop starts work again; a paused backlog
		// still waits for an explicit resume.
		a.state.QueuePaused = false
	}
	if command.Mode == "steer" && running && a.state.SteeringSupported {
		a.mu.Unlock()
		return a.steerLocked(text, command.ID)
	}
	if command.Mode == "queue" || running || a.state.Configuring {
		err := a.enqueueLocked(ACPQueuedPrompt{ID: command.ID, Text: text}, false)
		a.mu.Unlock()
		if err == nil {
			go a.drain()
		}
		return err
	}
	a.mu.Unlock()
	err := a.startPromptLocked(text, command.ID)
	if errors.Is(err, errACPNotIdle) {
		// A settings change began after the check above.
		a.mu.Lock()
		err = a.enqueueLocked(ACPQueuedPrompt{ID: command.ID, Text: text}, false)
		a.mu.Unlock()
		if err == nil {
			go a.drain()
		}
	}
	return err
}

// submittedLocked makes retried submissions idempotent across the transcript
// and the queue.
func (a *ACP) submittedLocked(id, text string) (bool, error) {
	if id == "" {
		return false, nil
	}
	for _, message := range a.state.Messages {
		if message.SubmissionID == id {
			if message.Text != text {
				return true, errors.New("submission ID already belongs to another message")
			}
			return true, nil
		}
	}
	for _, queued := range a.state.Queue {
		if queued.ID == id {
			if queued.Text != text {
				return true, errors.New("submission ID already belongs to another message")
			}
			return true, nil
		}
	}
	return false, nil
}

func (a *ACP) enqueueLocked(prompt ACPQueuedPrompt, front bool) error {
	if prompt.ID == "" {
		prompt.ID = "queued-" + rand.Text()
	}
	promptBytes := acpQueuedBytes(prompt)
	a.trimStateToBytesLocked(max(maxACPStateBytes-promptBytes, 0))
	if a.retainedStateBytesLocked()+promptBytes > maxACPStateBytes {
		return errors.New("the queue cannot retain another message")
	}
	if front {
		a.state.Queue = slices.Insert(a.state.Queue, 0, prompt)
	} else {
		a.state.Queue = append(a.state.Queue, prompt)
	}
	err := a.persistLocked()
	a.changedLocked()
	return err
}

func (a *ACP) unqueue(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	index := slices.IndexFunc(a.state.Queue, func(queued ACPQueuedPrompt) bool { return queued.ID == id })
	if index < 0 {
		return errors.New("message is no longer queued")
	}
	a.state.Queue = slices.Delete(a.state.Queue, index, index+1)
	if len(a.state.Queue) == 0 {
		a.state.QueuePaused = false
	}
	err := a.persistLocked()
	a.changedLocked()
	return err
}

func (a *ACP) resumeQueue() error {
	a.mu.Lock()
	a.state.QueuePaused = false
	a.changedLocked()
	a.mu.Unlock()
	go a.drain()
	return nil
}

// drain sends the head of the queue when the chat is idle. It sends one
// prompt; the end of that turn drains the next.
func (a *ACP) drain() {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.mu.Lock()
	if !a.state.Connected || a.state.Busy || a.state.Configuring || a.state.Steering ||
		a.state.QueuePaused || len(a.state.Queue) == 0 {
		a.mu.Unlock()
		return
	}
	next := a.state.Queue[0]
	a.mu.Unlock()
	if err := a.startPromptLocked(next.Text, next.ID); err != nil && !errors.Is(err, errACPNotIdle) {
		a.mu.Lock()
		a.state.QueuePaused = true
		a.changedLocked()
		a.mu.Unlock()
	}
}

// finishTurn records the end of a prompt turn. Only a normal end_turn keeps
// the queue running; cancellation, refusal, limits, and errors pause it so
// queued work never runs after the user stopped or the agent failed.
func (a *ACP) finishTurn(completed <-chan acpTurnResult) {
	result := <-completed
	a.mu.Lock()
	if result.err != nil {
		a.setErrorLocked(result.err)
	}
	if result.err != nil || result.stopReason != acpsdk.StopReasonEndTurn {
		a.state.QueuePaused = true
	}
	if a.external != nil {
		// A turn the agent started after a steer is still running.
		a.releaseHeldTextLocked()
		a.publishProgressLocked()
		a.mu.Unlock()
		return
	}
	a.endTurnLocked()
	a.mu.Unlock()
	a.drain()
}

// endTurnLocked settles what a finished turn leaves behind: open questions
// are cancelled and held text becomes visible.
func (a *ACP) endTurnLocked() {
	a.state.Busy = false
	a.state.Stopping = false
	a.turnCompleted = true
	a.clearPendingLocked()
	a.releaseHeldTextLocked()
	a.publishProgressLocked()
}

// clearPendingLocked answers every open permission and elicitation as
// cancelled.
func (a *ACP) clearPendingLocked() {
	for id, response := range a.permissions {
		response <- acpsdk.NewRequestPermissionOutcomeCancelled()
		delete(a.permissions, id)
	}
	a.state.Permissions = nil
	for id, response := range a.elicitations {
		response <- acpsdk.NewUnstableCreateElicitationResponseCancel()
		delete(a.elicitations, id)
	}
	a.state.Elicitations = nil
}

// setErrorLocked records err for the chat and keeps JSON-RPC error details.
func (a *ACP) setErrorLocked(err error) {
	a.state.Error, a.state.ErrorCode, a.state.ErrorData = "", nil, ""
	if err == nil {
		return
	}
	a.state.Error = boundText(err.Error(), maxACPErrorDataBytes)
	if requestErr, ok := errors.AsType[*acpsdk.RequestError](err); ok {
		a.state.Error = boundText(requestErr.Message, maxACPErrorDataBytes)
		a.state.ErrorCode = &requestErr.Code
		if requestErr.Data != nil {
			data, _ := json.Marshal(requestErr.Data, jsontext.WithIndent("  "))
			a.state.ErrorData = boundText(string(data), maxACPErrorDataBytes)
		}
	}
}

// steerLocked adds text to the running turn. The caller holds turnMu, so at
// most one steering request is in flight.
func (a *ACP) steerLocked(text, submissionID string) error {
	a.mu.Lock()
	a.state.Steering = true
	// Output that arrives while the agent takes the text follows it.
	a.releaseHeldTextLocked()
	a.promptIndex = new(len(a.state.Messages))
	// Only thread status reported during this steer describes a turn it may
	// start; an idle left over from an earlier turn must not hide it.
	a.threadStatus = ""
	a.changedLocked()
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), acpSteeringTimeout)
	defer cancel()
	raw, err := a.client.CallExtension(ctx, acpSteeringMethod, map[string]any{
		"sessionId": a.sessionID,
		"prompt":    []acpsdk.ContentBlock{acpsdk.TextBlock(text)},
		// An agent whose turn already ended asks for an ordinary prompt
		// instead of silently starting one Forge cannot track.
		"_meta": map[string]any{"steering": map[string]any{"idleBehavior": "promptRequired"}},
	})
	var result struct {
		Outcome string `json:"outcome"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	a.mu.Lock()
	a.state.Steering = false
	index := min(*a.promptIndex, len(a.state.Messages))
	a.promptIndex = nil
	if err != nil {
		// A failed steer must not let queued work run behind the user's back.
		a.state.QueuePaused = true
		a.changedLocked()
		a.mu.Unlock()
		return fmt.Errorf("steer ACP turn: %w", err)
	}
	switch result.Outcome {
	case "injected", "startedNewTurn":
		a.state.Messages = slices.Insert(a.state.Messages, index, ACPMessage{Role: "user", Text: text, SubmissionID: submissionID, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		if result.Outcome == "startedNewTurn" && a.threadStatus != "idle" {
			// The agent owns this turn; it ends when the thread goes idle,
			// whether or not the original prompt has completed yet.
			a.external = &acpExternalTurn{active: a.threadStatus == "active"}
			a.state.Busy = true
			if a.state.Stopping {
				// A stop requested during the steer applies to the new turn too.
				go func() {
					_ = a.client.Cancel(context.Background(), acpsdk.CancelNotification{SessionId: acpsdk.SessionId(a.sessionID)})
				}()
			}
		}
		a.trimStateLocked()
		err = a.persistLocked()
		a.changedLocked()
		a.mu.Unlock()
		return err
	case "promptRequired":
		// The turn ended before the text arrived; it runs next.
		err = a.enqueueLocked(ACPQueuedPrompt{ID: submissionID, Text: text}, true)
		a.mu.Unlock()
		if err == nil {
			go a.drain()
		}
		return err
	default:
		a.state.QueuePaused = true
		a.changedLocked()
		a.mu.Unlock()
		return fmt.Errorf("the agent did not accept the steering message (%q)", result.Outcome)
	}
}

func acpQueuedBytes(prompt ACPQueuedPrompt) int {
	data, _ := json.Marshal(prompt)
	return len(data) + 1
}
