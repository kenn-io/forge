package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"sync"
	"time"
)

type (
	ACPWatch  struct{ Revision uint64 }
	ACPUpdate struct {
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
}

func (o *acpOwnerRPC) Snapshot(_ struct{}, reply *ACPUpdate) error {
	o.agent.mu.Lock()
	defer o.agent.mu.Unlock()
	// Sorted map keys keep agent-provided objects, such as form schemas,
	// stable across snapshots.
	data, err := json.Marshal(o.agent.publishedStateLocked(), json.Deterministic(true))
	*reply = ACPUpdate{Revision: o.agent.revision, State: data, ExitCode: o.agent.exitCode}
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
// Code names a sentinel error; see acpErrorCode.
type ACPCommandReply struct {
	Unavailable bool
	Code        string
}

// acpErrorCodes maps the sentinel errors that cross the owner RPC to codes.
var acpErrorCodes = []struct {
	code string
	err  error
}{
	{"stale_request", ErrACPStaleRequest},
	{"uncertain", ErrACPSubmissionUncertain},
	{"busy", ErrACPBusy},
	{"supervised", ErrACPSupervised},
	{"stale_generation", ErrACPStaleGeneration},
	{"queue_pending", ErrACPQueuePending},
}

// acpErrorCode returns the code for a sentinel error, or "" for any other.
func acpErrorCode(err error) string {
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

// ACPHistoryRequest selects transcript messages [Before-Limit, Before).
type ACPHistoryRequest struct{ Before, Limit int }

func (o *acpOwnerRPC) History(request ACPHistoryRequest, reply *[]byte) error {
	data, err := o.agent.History(request.Before, request.Limit)
	*reply = data
	return err
}

func (o *acpOwnerRPC) Command(command ACPCommand, reply *ACPCommandReply) error {
	err := o.agent.Command(command)
	if errors.Is(err, ErrACPAgentUnavailable) {
		reply.Unavailable = true
		return nil
	}
	if code := acpErrorCode(err); code != "" {
		reply.Code = code
		return nil
	}
	return err
}

func (o *acpOwnerRPC) Bind(binding ACPMCPBinding, _ *struct{}) error { return o.proxy.Bind(binding) }

func (o *acpOwnerRPC) Stop(_ struct{}, _ *struct{}) error {
	o.once.Do(func() {
		close(o.stop)
	})
	<-o.stopped
	return o.stopErr
}

type acpAttachment struct {
	address     string
	client      *rpc.Client
	done        chan struct{}
	mu          sync.Mutex
	exitCode    int
	subscribers map[chan struct{}]struct{}
}

func (a *acpAttachment) call(ctx context.Context, method string, args, reply any) error {
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
	err := a.client.Call("ACP.Snapshot", struct{}{}, &reply)
	return reply.State, err
}

func (a *acpAttachment) Command(command ACPCommand) error {
	var reply ACPCommandReply
	if err := a.client.Call("ACP.Command", command, &reply); err != nil {
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
	return nil
}

func (a *acpAttachment) History(before, limit int) ([]byte, error) {
	var reply []byte
	err := a.client.Call("ACP.History", ACPHistoryRequest{Before: before, Limit: limit}, &reply)
	return reply, err
}

func (a *acpAttachment) Prompt(text string) error {
	return a.Command(ACPCommand{Type: "prompt", Text: text})
}
func (a *acpAttachment) Done() <-chan struct{} { return a.done }
func (a *acpAttachment) ExitCode() int         { a.mu.Lock(); defer a.mu.Unlock(); return a.exitCode }
func (a *acpAttachment) Detach()               { _ = a.client.Close() }
func (a *acpAttachment) Stop(ctx context.Context) error {
	// The watch connection closes as soon as the agent exits. Use an independent
	// request connection so that cannot discard the stop acknowledgement.
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", a.address)
	if err != nil {
		return err
	}
	request := &acpAttachment{client: rpc.NewClient(conn)}
	defer request.client.Close()
	err = request.call(ctx, "ACP.Stop", struct{}{}, &struct{}{})
	a.Detach()
	return err
}

func (a *acpAttachment) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	a.mu.Lock()
	a.subscribers[ch] = struct{}{}
	ch <- struct{}{}
	a.mu.Unlock()
	return ch, func() { a.mu.Lock(); delete(a.subscribers, ch); a.mu.Unlock() }
}

func (a *acpAttachment) watch() {
	defer close(a.done)
	defer a.client.Close()
	var revision uint64
	for {
		var reply ACPUpdate
		err := a.client.Call("ACP.Watch", ACPWatch{Revision: revision}, &reply)
		a.mu.Lock()
		a.exitCode = reply.ExitCode
		if err != nil || reply.Revision != revision || reply.Exited {
			for ch := range a.subscribers {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
		a.mu.Unlock()
		if err != nil || reply.Exited {
			return
		}
		revision = reply.Revision
	}
}
