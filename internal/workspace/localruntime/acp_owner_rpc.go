package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/rpc"
	"sync"
	"time"
)

// Increment when the owner chat frames or RPC contracts change incompatibly.
// Owners outlive the daemon; attaching a new UI must not reuse an old decoder.
const acpOwnerProtocol = 1

type (
	ACPWatch  struct{ Revision uint64 }
	ACPUpdate struct {
		Protocol int
		Revision uint64
		State    []byte
		Exited   bool
		ExitCode int
	}
)

type acpOwnerRPC struct {
	agent   *ACP
	proxy   *acpMCPProxy
	stop    chan struct{}
	stopped chan struct{}
	stopErr error
	once    sync.Once
	// park keeps the saved session for a replacement owner.
	park bool
}

func (o *acpOwnerRPC) Snapshot(_ struct{}, reply *ACPUpdate) error {
	o.agent.mu.Lock()
	defer o.agent.mu.Unlock()
	// Sorted map keys keep agent-provided objects, such as form schemas,
	// stable across snapshots.
	data, err := json.Marshal(o.agent.publishedStateLocked(), json.Deterministic(true))
	*reply = ACPUpdate{Protocol: acpOwnerProtocol, Revision: o.agent.revision, State: data, ExitCode: o.agent.exitCode}
	select {
	case <-o.agent.done:
		reply.Exited = true
	default:
	}
	return err
}

func (o *acpOwnerRPC) Watch(request ACPWatch, reply *ACPUpdate) error {
	changes, unsubscribe := o.agent.Subscribe()
	defer unsubscribe()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		o.agent.mu.Lock()
		*reply = ACPUpdate{Revision: o.agent.revision, ExitCode: o.agent.exitCode}
		o.agent.mu.Unlock()
		select {
		case <-o.agent.done:
			reply.Exited = true
		default:
		}
		if reply.Revision != request.Revision || reply.Exited {
			return nil
		}
		select {
		case <-changes:
		case <-o.agent.done:
		case <-timer.C:
			reply.State = nil
			return nil
		}
	}
}

// ACPCommandReply types the command failures callers branch on. net/rpc
// reduces returned errors to text, so they travel as successful replies.
// Code names a sentinel error; see ACPErrorCode.
type ACPCommandReply struct {
	Unavailable bool
	Code        string
}

// acpErrorCodes maps the sentinel errors that cross the owner RPC to codes. An
// owner that fails to start records its code instead; see acpStartErrorFile.
var acpErrorCodes = []struct {
	code string
	err  error
}{
	{"stale_request", ErrACPStaleRequest},
	{"supervised", ErrACPSupervised},
	{"stale_generation", ErrACPStaleGeneration},
	{"not_owned", ErrACPNotOwned},
	{"person_only", ErrACPPersonOnly},
	{"cannot_reload", ErrACPCannotReload},
	{"withdrawn", errACPSubmissionWithdrawn},
	{"sending", errACPQueuedPromptSending},
	{"already_answered", errACPAlreadyAnswered},
	{"not_pending", errACPNotPending},
}

// ACPErrorCode returns the code for a sentinel error, or "" for any other.
func ACPErrorCode(err error) string {
	for _, entry := range acpErrorCodes {
		if errors.Is(err, entry.err) {
			return entry.code
		}
	}
	return ""
}

// acpCodeError restores the sentinel error for a code, or nil for an unknown one.
func acpCodeError(code string) error {
	for _, entry := range acpErrorCodes {
		if entry.code == code {
			return entry.err
		}
	}
	return nil
}

// ACPHistoryRequest selects messages preceding a stable transcript entry ID.
type ACPHistoryRequest struct {
	BeforeID string
	Limit    int
}

func (o *acpOwnerRPC) History(request ACPHistoryRequest, reply *[]byte) error {
	data, err := o.agent.History(request.BeforeID, request.Limit)
	*reply = data
	return err
}

func (o *acpOwnerRPC) Updates(after uint64, reply *ACPBatch) error {
	changes, err := o.agent.Updates(after)
	*reply = changes
	return err
}

// ACPPageRequest selects transcript messages [After, After+Limit).
type ACPPageRequest struct{ After, Limit int }

func (o *acpOwnerRPC) Page(request ACPPageRequest, reply *[]byte) error {
	data, err := o.agent.Page(request.After, request.Limit)
	*reply = data
	return err
}

func (o *acpOwnerRPC) Command(command ACPCommand, reply *ACPCommandReply) error {
	err := o.agent.Command(command)
	if errors.Is(err, ErrACPAgentUnavailable) {
		reply.Unavailable = true
		return nil
	}
	if code := ACPErrorCode(err); code != "" {
		reply.Code = code
		return nil
	}
	return err
}

func (o *acpOwnerRPC) Bind(binding ACPMCPBinding, _ *struct{}) error { return o.proxy.Bind(binding) }

func (o *acpOwnerRPC) Stop(_ struct{}, _ *struct{}) error {
	return o.shutdown(false)
}

// Park stops the agent like Stop but keeps its saved session, so reopening
// the workspace loads it into a new owner.
func (o *acpOwnerRPC) Park(_ struct{}, _ *struct{}) error {
	return o.shutdown(true)
}

func (o *acpOwnerRPC) shutdown(park bool) error {
	o.once.Do(func() {
		o.park = park
		close(o.stop)
	})
	<-o.stopped
	return o.stopErr
}

type acpAttachment struct {
	// workspaceID and key name the chat in the daemon log.
	workspaceID string
	key         string
	client      *rpc.Client
	done        chan struct{}
	doneOnce    sync.Once
	mu          sync.Mutex
	exitCode    int
	exited      bool
	stopping    bool
	pending     int
	subscribers map[chan struct{}]struct{}
}

func (a *acpAttachment) call(ctx context.Context, method string, args, reply any) error {
	a.mu.Lock()
	a.pending++
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.pending--
		a.closeIfDrainedLocked()
	}()
	call := a.client.Go(method, args, reply, make(chan *rpc.Call, 1))
	select {
	case completed := <-call.Done:
		return completed.Error
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *acpAttachment) Snapshot() ([]byte, error) {
	var reply ACPUpdate
	err := a.call(context.Background(), "ACP.Snapshot", struct{}{}, &reply)
	return reply.State, err
}

func (a *acpAttachment) Command(command ACPCommand) error {
	var reply ACPCommandReply
	if err := a.call(context.Background(), "ACP.Command", command, &reply); err != nil {
		return err
	}
	if reply.Unavailable {
		return ErrACPAgentUnavailable
	}
	if reply.Code != "" {
		if err := acpCodeError(reply.Code); err != nil {
			return err
		}
		return fmt.Errorf("owner returned unknown error code %q", reply.Code)
	}
	if command.Type == "prompt" {
		// Every prompt reaches its chat through an attachment, so the
		// daemon log records who sent each one.
		slog.Info("ACP prompt submitted",
			"workspace_id", a.workspaceID, "session_key", a.key, "submission_id", command.ID,
			"mode", command.Mode, "sender", command.SenderKind(), "generation", command.Generation)
	}
	return nil
}

func (a *acpAttachment) History(beforeID string, limit int) ([]byte, error) {
	var reply []byte
	err := a.call(context.Background(), "ACP.History", ACPHistoryRequest{BeforeID: beforeID, Limit: limit}, &reply)
	return reply, err
}

func (a *acpAttachment) Updates(after uint64) (ACPBatch, error) {
	var reply ACPBatch
	err := a.call(context.Background(), "ACP.Updates", after, &reply)
	return reply, err
}

func (a *acpAttachment) Page(after, limit int) ([]byte, error) {
	var reply []byte
	err := a.call(context.Background(), "ACP.Page", ACPPageRequest{After: after, Limit: limit}, &reply)
	return reply, err
}

func (a *acpAttachment) Prompt(text string, sender ACPSender) error {
	return a.Command(ACPCommand{Type: "prompt", Text: text, FromAgent: sender.coordinator()})
}
func (a *acpAttachment) Done() <-chan struct{} { return a.done }
func (a *acpAttachment) ExitCode() int         { a.mu.Lock(); defer a.mu.Unlock(); return a.exitCode }

func (a *acpAttachment) Detach() {
	_ = a.client.Close()
	a.doneOnce.Do(func() { close(a.done) })
}

func (a *acpAttachment) Stop(ctx context.Context) error {
	// Keep the existing connection through the stop acknowledgement: after
	// natural exit the listener is closed while this viewer drains final updates.
	a.mu.Lock()
	a.stopping = true
	a.mu.Unlock()
	err := a.call(ctx, "ACP.Stop", struct{}{}, &struct{}{})
	a.Detach()
	return err
}

func (a *acpAttachment) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	a.mu.Lock()
	a.subscribers[ch] = struct{}{}
	ch <- struct{}{}
	a.mu.Unlock()
	return ch, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.subscribers, ch)
		a.closeIfDrainedLocked()
	}
}

// Natural exit drains both viewer updates and in-flight RPC replies.
func (a *acpAttachment) closeIfDrainedLocked() {
	if a.exited && !a.stopping && a.pending == 0 && len(a.subscribers) == 0 {
		_ = a.client.Close()
		a.doneOnce.Do(func() { close(a.done) })
	}
}

func (a *acpAttachment) watch() {
	var revision uint64
	for {
		var reply ACPUpdate
		err := a.client.Call("ACP.Watch", ACPWatch{Revision: revision}, &reply)
		a.mu.Lock()
		a.exitCode = reply.ExitCode
		a.exited = err != nil || reply.Exited
		if err != nil || reply.Revision != revision || reply.Exited {
			for ch := range a.subscribers {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
		// Final updates may need several batches. Existing viewers keep the
		// read connection until they finish draining updates and unsubscribe.
		if err != nil {
			_ = a.client.Close()
			a.doneOnce.Do(func() { close(a.done) })
		} else {
			a.closeIfDrainedLocked()
		}
		a.mu.Unlock()
		if err != nil || reply.Exited {
			return
		}
		revision = reply.Revision
	}
}
