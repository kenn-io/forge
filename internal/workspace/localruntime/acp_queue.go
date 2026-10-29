package localruntime

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// acpSteeringMethod is the agent extension that adds user input to a running
// turn. Agents advertise it with initialize _meta.steering.supported.
const acpSteeringMethod = "_session/steering"

type acpTurnResult struct {
	stopReason acpsdk.StopReason
	err        error
}

// submit never rejects input because a turn is running: a busy chat queues
// the prompt, or steers it into the turn when a person asks and the agent
// supports it. Coordinators' prompts queue like people's.
func (a *ACP) submit(command ACPCommand) error {
	text := command.Text
	if strings.TrimSpace(text) == "" && len(command.Images) == 0 {
		return errors.New("message must not be empty")
	}
	if len(command.Images) > 0 && !a.imagesSupported {
		return errors.New("this agent does not accept image prompts")
	}
	switch command.Mode {
	case "", "send", "queue", "steer":
	default:
		return errors.New("prompt mode must be send, queue, or steer")
	}
	if command.AllowanceMillis < 0 {
		return errACPNegativeAllowance
	}
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.mu.Lock()
	sender, err := a.promptSenderLocked(command)
	if err == nil && command.AllowanceMillis > 0 && command.Generation == 0 {
		err = errACPUnsupervisedAllowance
	}
	if err != nil {
		a.mu.Unlock()
		return err
	}
	if submitted, err := a.submittedLocked(command.ID, text, command.Images); submitted || err != nil {
		a.mu.Unlock()
		return err
	}
	if !a.state.Connected {
		a.mu.Unlock()
		return ErrACPAgentUnavailable
	}
	prompt := ACPQueuedPrompt{
		ID: command.ID, Text: text, Images: command.Images, Sender: sender,
		Generation: command.Generation, Allowance: command.AllowanceMillis,
	}
	running := a.state.Busy || a.state.Steering
	if !running && len(a.state.Queue) == 0 {
		// A new message after a stop starts work again; a paused backlog
		// still waits for an explicit resume.
		a.state.QueuePaused = false
	}
	if command.Mode == "steer" && running && a.state.SteeringSupported {
		a.acceptSteerLocked(prompt)
		a.mu.Unlock()
		return nil
	}
	if command.Mode == "queue" || running || a.state.Configuring {
		a.enqueueLocked(prompt, false)
		a.mu.Unlock()
		go a.drain()
		return nil
	}
	a.mu.Unlock()
	if a.beforePromptStart != nil {
		a.beforePromptStart()
	}
	return a.startPromptLocked(prompt, false)
}

// submittedLocked makes retried submissions idempotent across the transcript,
// the queue, and the steers.
func (a *ACP) submittedLocked(id, text string, images []ACPContent) (bool, error) {
	if id == "" {
		return false, nil
	}
	if slices.Contains(a.state.Withdrawn, id) {
		return true, errACPSubmissionWithdrawn
	}
	for _, message := range a.state.Messages {
		if message.SubmissionID == id {
			if message.Text != text || !slices.Equal(message.Images, images) {
				return true, errors.New("submission ID already belongs to another message")
			}
			return true, nil
		}
	}
	for _, queued := range slices.Concat(a.state.Queue, a.state.Steers) {
		if queued.ID == id {
			if queued.Text != text || !slices.Equal(queued.Images, images) {
				return true, errors.New("submission ID already belongs to another message")
			}
			return true, nil
		}
	}
	return false, nil
}

// enqueueLocked queues prompt. A queued prompt drains even if saving the
// queue fails, so the save cannot refuse it.
func (a *ACP) enqueueLocked(prompt ACPQueuedPrompt, front bool) {
	if prompt.ID == "" {
		prompt.ID = "queued-" + rand.Text()
	}
	if front {
		a.state.Queue = slices.Insert(a.state.Queue, 0, prompt)
	} else {
		a.state.Queue = append(a.state.Queue, prompt)
	}
	a.saveTakenEffectLocked("queueing a prompt")
	a.changedLocked()
}

// errACPQueuedPromptSending refuses to withdraw or move a queued prompt that
// is already being sent.
var errACPQueuedPromptSending = errors.New("message is already being sent")

// queuedIndexLocked returns the queue index of the prompt id. A coordinator
// may act only on a prompt it submitted under the generation it holds. A
// prompt being sent stays at the head of the queue until its write returns,
// but the agent may already have it, so it can no longer be removed or moved.
func (a *ACP) queuedIndexLocked(id string, generation uint64) (int, error) {
	if err := a.supervisionGateLocked(generation); err != nil {
		return -1, err
	}
	index := slices.IndexFunc(a.state.Queue, func(queued ACPQueuedPrompt) bool { return queued.ID == id })
	if index < 0 {
		return -1, errors.New("message is no longer queued")
	}
	if generation != 0 && a.state.Queue[index].Generation != generation {
		return -1, ErrACPNotOwned
	}
	if a.sendingLocked(id) {
		return -1, errACPQueuedPromptSending
	}
	return index, nil
}

// sendingLocked reports whether the prompt id is being written to the agent.
func (a *ACP) sendingLocked(id string) bool {
	return a.state.Sending != nil && a.state.Sending.ID == id
}

func (a *ACP) unqueue(id string, generation uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	index, err := a.queuedIndexLocked(id, generation)
	if err != nil {
		return err
	}
	a.state.Queue = slices.Delete(a.state.Queue, index, index+1)
	// A retry of a withdrawn submission must not run the work after all.
	a.state.Withdrawn = append(a.state.Withdrawn, id)
	if len(a.state.Queue) == 0 {
		a.state.QueuePaused = false
	}
	a.saveTakenEffectLocked("unqueueing a prompt")
	a.changedLocked()
	return nil
}

// move puts a queued prompt at position index, clamped to the queue. Only
// people order the queue, so they can put their own work ahead of a
// coordinator's. Nothing moves ahead of a prompt being sent.
func (a *ACP) move(id string, index int, generation uint64) error {
	if err := personGate(generation); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	from, err := a.queuedIndexLocked(id, 0)
	if err != nil {
		return err
	}
	prompt := a.state.Queue[from]
	a.state.Queue = slices.Delete(a.state.Queue, from, from+1)
	first := 0
	if len(a.state.Queue) > 0 && a.sendingLocked(a.state.Queue[0].ID) {
		first = 1
	}
	a.state.Queue = slices.Insert(a.state.Queue, max(first, min(index, len(a.state.Queue))), prompt)
	a.saveTakenEffectLocked("reordering the queue")
	a.changedLocked()
	return nil
}

// resumeQueue runs a paused queue again. Queued work pauses after a stop or
// a failed turn, so only a person decides that it runs.
func (a *ACP) resumeQueue(generation uint64) error {
	if err := personGate(generation); err != nil {
		return err
	}
	a.mu.Lock()
	a.state.QueuePaused = false
	a.changedLocked()
	a.mu.Unlock()
	go a.drain()
	return nil
}

// drain sends the head of the queue when the chat is idle. It sends one
// prompt; the end of that turn drains the next. A head withdrawn or moved
// back while drain was reading it is skipped for the new head.
func (a *ACP) drain() {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	for {
		a.mu.Lock()
		a.dropUnrunnableQueueHeadLocked()
		if !a.state.Connected || a.state.Busy || a.state.Configuring || a.state.Steering ||
			a.state.QueuePaused || len(a.state.Queue) == 0 {
			a.mu.Unlock()
			return
		}
		next := a.state.Queue[0]
		a.mu.Unlock()
		err := a.startPromptLocked(next, true)
		if errors.Is(err, errACPQueueChanged) {
			continue
		}
		if err != nil && !errors.Is(err, errACPNotIdle) {
			a.mu.Lock()
			a.setErrorLocked(err)
			a.state.QueuePaused = true
			a.changedLocked()
			a.mu.Unlock()
		}
		return
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
	if a.external != nil && (result.err != nil || result.stopReason != acpsdk.StopReasonEndTurn) {
		// A stop or failure ends the agent's own turn too; waiting for an
		// idle that may never come would leave the chat busy.
		a.external = nil
	}
	a.takeoverPending = a.takeoverPending && result.err == nil && result.stopReason == acpsdk.StopReasonEndTurn
	if a.external != nil {
		// A turn the agent started after a steer is still running.
		a.external.promptDone = true
		a.releaseHeldTextLocked()
		a.publishProgressLocked()
		a.mu.Unlock()
		return
	}
	a.endTurnLocked(string(result.stopReason), result.err)
	a.mu.Unlock()
	a.drain()
}

// endTurnLocked settles what a finished turn leaves behind: its record ends,
// open questions are cancelled, and held text becomes visible.
func (a *ACP) endTurnLocked(stopReason string, err error) {
	a.endTurnRecordLocked(stopReason, err)
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
	a.refreshBlockedLocked()
}

// setErrorLocked records err for the chat and keeps JSON-RPC error details.
func (a *ACP) setErrorLocked(err error) {
	a.state.Error, a.state.ErrorCode, a.state.ErrorData = "", nil, ""
	if err == nil {
		return
	}
	a.state.Error = err.Error()
	if requestErr, ok := errors.AsType[*acpsdk.RequestError](err); ok {
		a.state.Error = requestErr.Message
		a.state.ErrorCode = &requestErr.Code
		if requestErr.Data != nil {
			data, _ := json.Marshal(requestErr.Data, jsontext.WithIndent("  "))
			a.state.ErrorData = string(data)
		}
	}
}

// acceptSteerLocked accepts a person's prompt to steer the running turn.
// The steer is saved and published at once; the steering loop hands steers
// to the agent one at a time, in order, and starts with the first.
func (a *ACP) acceptSteerLocked(prompt ACPQueuedPrompt) {
	if prompt.ID == "" {
		prompt.ID = "steer-" + rand.Text()
	}
	a.state.Steers = append(a.state.Steers, prompt)
	a.state.Steering = true
	a.saveTakenEffectLocked("accepting a steer")
	a.changedLocked()
	if len(a.state.Steers) == 1 {
		go a.steer()
	}
}

// steer hands the accepted steers to the agent, oldest first, while the turn
// runs. It runs while Steers is not empty. An accepted steer always runs: the
// agent takes it into the turn, or it and every steer behind it move to the
// front of the queue and run as the next prompts. Nothing times a steer out;
// the agent answers when it takes the text or its turn ends, and a closed
// connection fails the request.
func (a *ACP) steer() {
	defer a.drain()
	for {
		a.mu.Lock()
		if !a.state.Connected || !a.state.Busy {
			// The turn ended before this steer reached the agent.
			a.requeueSteersLocked()
			a.mu.Unlock()
			return
		}
		prompt := a.state.Steers[0]
		// Output that arrives while the agent takes the text follows it.
		a.releaseHeldTextLocked()
		a.promptIndex = new(len(a.state.Messages))
		// Only thread status reported during this steer describes a turn it may
		// start; an idle left over from an earlier turn must not hide it.
		a.threadStatus = ""
		a.changedLocked()
		a.mu.Unlock()
		if a.beforePromptWrite != nil {
			a.beforePromptWrite()
		}
		raw, err := a.client.CallExtension(context.Background(), acpSteeringMethod, map[string]any{
			"sessionId": a.sessionID,
			"prompt":    acpPromptContent(prompt.Text, prompt.Images),
			// An agent whose turn already ended asks for an ordinary prompt
			// instead of silently starting one Forge cannot track.
			"_meta": map[string]any{
				"steering":   map[string]any{"idleBehavior": "promptRequired"},
				acpForgeMeta: acpPromptMeta(prompt),
			},
		})
		var result struct {
			Outcome string `json:"outcome"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &result)
		}
		// A turn the agent starts here begins under turnMu, as every turn does.
		a.turnMu.Lock()
		a.mu.Lock()
		index := min(*a.promptIndex, len(a.state.Messages))
		a.promptIndex = nil
		if err != nil || (result.Outcome != "injected" && result.Outcome != "startedNewTurn") {
			if err != nil || result.Outcome != "promptRequired" {
				slog.Warn("ACP agent did not take a steer; it runs as the next prompt",
					"submission_id", prompt.ID, "outcome", result.Outcome, "error", err)
			}
			a.requeueSteersLocked()
			a.mu.Unlock()
			a.turnMu.Unlock()
			return
		}
		a.state.Steers = a.state.Steers[1:]
		a.state.Steering = len(a.state.Steers) > 0
		a.insertMessageLocked(index, promptMessage(prompt))
		if result.Outcome == "startedNewTurn" {
			// The agent owns this turn; it ends when the thread goes idle,
			// whether or not the original prompt has completed yet. Until a
			// thread status confirms it, the turn only waits for one: an agent
			// that never reports status ends it with the prompt rather than
			// leaving the chat busy for good.
			if a.reportsThreadStatus && a.threadStatus != "idle" {
				a.external = &acpExternalTurn{active: a.threadStatus == "active", promptDone: !a.state.Busy}
				a.state.Busy = true
				if a.runningTurnRecordLocked() == nil {
					// The prompt's turn ended before the agent answered.
					a.beginTurnRecordLocked(prompt, index)
				}
			} else if !a.reportsThreadStatus {
				a.takeoverPending = true
			}
			if a.state.Stopping {
				// A stop requested during the steer applies to the new turn too.
				go func() {
					_ = a.client.Cancel(context.Background(), acpsdk.CancelNotification{SessionId: acpsdk.SessionId(a.sessionID)})
				}()
			}
		}
		a.saveTakenEffectLocked("steering")
		a.changedLocked()
		done := !a.state.Steering
		a.mu.Unlock()
		a.turnMu.Unlock()
		if done {
			return
		}
	}
}

// requeueSteersLocked moves every steer the agent has not taken to the front
// of the queue, in order, so each runs as a next prompt. Nothing is being
// sent from the queue while a steer is pending. A queue left behind by an
// exited agent waits for a person, as on any exit.
func (a *ACP) requeueSteersLocked() {
	a.state.Queue = slices.Insert(a.state.Queue, 0, a.state.Steers...)
	a.state.Steers = nil
	a.state.Steering = false
	if !a.state.Busy {
		// A stop pressed after the turn ended was waiting only on the steer.
		a.state.Stopping = false
	}
	if !a.state.Connected {
		a.state.QueuePaused = true
	}
	a.saveTakenEffectLocked("queueing steers the agent did not take")
	a.changedLocked()
}

// acpForgeMeta is the _meta key under which Forge describes a prompt to the
// agent. ACP has no notion of several senders, so it names who sent it.
const acpForgeMeta = "forge"

// acpPromptMeta describes prompt under acpForgeMeta.
func acpPromptMeta(prompt ACPQueuedPrompt) map[string]any {
	meta := map[string]any{"sender": prompt.Sender.Kind}
	if prompt.Sender.Name != "" {
		meta["senderName"] = prompt.Sender.Name
	}
	if prompt.ID != "" {
		meta["submissionId"] = prompt.ID
	}
	return meta
}

// promptMessage is the transcript entry of a prompt the agent received.
func promptMessage(prompt ACPQueuedPrompt) ACPMessage {
	sender := prompt.Sender
	return ACPMessage{
		Role: "user", Text: prompt.Text, Images: prompt.Images, SubmissionID: prompt.ID,
		Sender: &sender, CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// acpPromptContent keeps pasted images with the text in prompts and steers.
func acpPromptContent(text string, images []ACPContent) []acpsdk.ContentBlock {
	blocks := make([]acpsdk.ContentBlock, 0, 1+len(images))
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, acpsdk.TextBlock(text))
	}
	for _, image := range images {
		blocks = append(blocks, acpsdk.ImageBlock(image.Data, image.MimeType))
	}
	return blocks
}
