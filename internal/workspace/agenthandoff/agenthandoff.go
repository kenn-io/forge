// Package agenthandoff owns the polling contract shared by every path that
// hands a first prompt to a freshly launched agent: waiting for the workspace
// to become ready, retrying delivery while the agent's input mode is not
// ready, and sleeping between attempts without outliving the caller's
// context. The workspace HTTP endpoint and the MCP spawn tool both drive it
// with their own transport calls and map its errors into their own contracts.
package agenthandoff

import (
	"context"
	"errors"
	"strings"
	"time"
)

// DefaultPollInterval is the sleep between attempts when a Poller has none.
const DefaultPollInterval = 250 * time.Millisecond

// ErrWorkspaceSetupFailed reports a workspace whose provisioning ended in the
// error status. The wrapped message carries the workspace's own error text.
var ErrWorkspaceSetupFailed = errors.New("workspace setup failed")

// Poller paces retry loops. The zero value uses DefaultPollInterval.
type Poller struct {
	Interval time.Duration
}

// WorkspaceState is the readiness view of a workspace that the wait needs.
type WorkspaceState struct {
	Status       string
	ErrorMessage string
}

// Wait sleeps one interval. When ctx ends first it returns context.Cause(ctx)
// so callers can tell a deadline from a deliberate cancellation.
func (p Poller) Wait(ctx context.Context) error {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// Until runs step until it reports done or fails, sleeping one interval
// between attempts.
func (p Poller) Until(ctx context.Context, step func(context.Context) (bool, error)) error {
	for {
		done, err := step(ctx)
		if err != nil || done {
			return err
		}
		if err := p.Wait(ctx); err != nil {
			return err
		}
	}
}

// WaitForWorkspace polls get until the workspace reports the ready status.
// The error status ends the wait with ErrWorkspaceSetupFailed; every other
// status keeps polling.
func (p Poller) WaitForWorkspace(
	ctx context.Context, get func(context.Context) (WorkspaceState, error),
) error {
	return p.Until(ctx, func(ctx context.Context) (bool, error) {
		state, err := get(ctx)
		if err != nil {
			return false, err
		}
		switch state.Status {
		case "ready":
			return true, nil
		case "error":
			if message := strings.TrimSpace(state.ErrorMessage); message != "" {
				return false, errors.Join(ErrWorkspaceSetupFailed, errors.New(message))
			}
			return false, ErrWorkspaceSetupFailed
		}
		return false, nil
	})
}

// Deliver calls submit until it succeeds or fails with an error that
// retryable rejects. A retryable error means nothing reached the agent, so
// the same runtime can be tried again after one interval. The last result is
// returned alongside a wait error so callers can report the state they saw.
func Deliver[T any](
	ctx context.Context, p Poller,
	submit func(context.Context) (T, error), retryable func(error) bool,
) (T, error) {
	for {
		result, err := submit(ctx)
		if err == nil || !retryable(err) {
			return result, err
		}
		if err := p.Wait(ctx); err != nil {
			return result, err
		}
	}
}
