package localruntime

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// DormantACP is a stored chat whose agent is not running. It serves the
// saved transcript and state at once, and the first command that needs the
// agent starts it, then runs on the started chat, as do later commands,
// history, and pages. Snapshot, Updates, and Subscribe stay on the saved
// state: a reader switches to Live once Started closes, since the two count
// changes differently.
type DormantACP struct {
	saved *ACP
	start func() (ACPChat, error)

	startMu sync.Mutex // one start at a time
	mu      sync.Mutex
	live    ACPChat
	started chan struct{}
}

// DormantACP returns the chat key of workspaceID served from its saved
// session. start starts its agent and returns the running chat. It returns
// ErrSessionNotFound when the workspace has no such chat, and
// ErrSessionUnavailable when the chat saved no session.
func (m *Manager) DormantACP(workspaceID, key string, start func() (ACPChat, error)) (*DormantACP, error) {
	if m.acpSessionsDir == "" {
		return nil, ErrSessionNotFound
	}
	if err := m.requireACPWorkspace(workspaceID, key); err != nil {
		return nil, err
	}
	saved, err := m.readSavedACP(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: chat %q saved no session", ErrSessionUnavailable, key)
	}
	if err != nil {
		return nil, fmt.Errorf("read saved ACP session: %w", err)
	}
	return newDormantACP(saved.State, start), nil
}

// newDormantACP shows saved as a reload would, without requests the stopped
// agent can no longer answer.
func newDormantACP(saved ACPState, start func() (ACPChat, error)) *DormantACP {
	view := &ACP{done: make(chan struct{}), subscribers: make(map[chan struct{}]struct{}), exitCode: -1}
	view.restoreTranscriptLocked(saved)
	view.state.ConfigOptions = saved.ConfigOptions
	view.state.Dormant = true
	return &DormantACP{saved: view, start: start, started: make(chan struct{})}
}

// Started closes once the chat's agent runs; Live then returns it.
func (d *DormantACP) Started() <-chan struct{} { return d.started }

// Live returns the started chat, or nil before it starts.
func (d *DormantACP) Live() ACPChat {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.live
}

func (d *DormantACP) Snapshot() ([]byte, error)              { return d.saved.Snapshot() }
func (d *DormantACP) Updates(after uint64) (ACPBatch, error) { return d.saved.Updates(after) }
func (d *DormantACP) Subscribe() (<-chan struct{}, func())   { return d.saved.Subscribe() }

// History pages the saved transcript until the agent starts. The started chat
// keeps the saved message IDs, so a page asked for before the start is found.
func (d *DormantACP) History(beforeID string, limit int) ([]byte, error) {
	if live := d.Live(); live != nil {
		return live.History(beforeID, limit)
	}
	return d.saved.History(beforeID, limit)
}

func (d *DormantACP) Page(after, limit int) ([]byte, error) {
	if live := d.Live(); live != nil {
		return live.Page(after, limit)
	}
	return d.saved.Page(after, limit)
}

// Command starts the agent and runs command on it. Cancelling and dismissing
// an error need no agent: nothing runs to cancel, and the error is this
// view's own.
func (d *DormantACP) Command(command ACPCommand) error {
	if live := d.Live(); live != nil {
		return live.Command(command)
	}
	switch command.Type {
	case "cancel":
		return nil
	case "dismiss_error":
		d.update(func(state *ACP) { state.setErrorLocked(nil) })
		return nil
	}
	live, err := d.ensureStarted()
	if err != nil {
		return err
	}
	return live.Command(command)
}

func (d *DormantACP) ensureStarted() (ACPChat, error) {
	d.startMu.Lock()
	defer d.startMu.Unlock()
	if live := d.Live(); live != nil {
		return live, nil
	}
	d.update(func(state *ACP) {
		state.setErrorLocked(nil)
		state.state.Starting = true
	})
	live, err := d.start()
	if err != nil {
		failure := &ACPStartError{err: err}
		d.update(func(state *ACP) {
			state.state.Starting = false
			state.setErrorLocked(failure)
		})
		return nil, failure
	}
	d.mu.Lock()
	d.live = live
	close(d.started)
	d.mu.Unlock()
	return live, nil
}

func (d *DormantACP) update(change func(*ACP)) {
	d.saved.mu.Lock()
	defer d.saved.mu.Unlock()
	change(d.saved)
	d.saved.changedLocked()
}

// ACPStartError is a stored chat whose agent failed to start. Its message is
// what a person reads, with the cause's ACPStartReasonError when it has one;
// the cause it wraps holds the technical detail.
type ACPStartError struct{ err error }

func (e *ACPStartError) Error() string {
	if reason, ok := errors.AsType[ACPStartReasonError](e.err); ok {
		return "The agent did not start: " + string(reason) + "."
	}
	return "The agent did not start."
}

func (e *ACPStartError) Unwrap() error { return e.err }

// ACPStartReasonError is a start failure written for the person using the
// chat, which a failed start shows as its summary.
type ACPStartReasonError string

func (r ACPStartReasonError) Error() string { return string(r) }
