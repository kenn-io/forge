package agenthandoff

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitReturnsCancellationCause(t *testing.T) {
	assert := assert.New(t)
	cause := errors.New("shutdown")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)

	err := Poller{Interval: time.Hour}.Wait(ctx)

	assert.ErrorIs(err, cause)
}

func TestWaitForWorkspacePollsUntilReady(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	statuses := []string{"creating", "provisioning", "ready"}
	calls := 0

	err := Poller{Interval: time.Millisecond}.WaitForWorkspace(
		t.Context(), func(context.Context) (WorkspaceState, error) {
			state := WorkspaceState{Status: statuses[calls]}
			calls++
			return state, nil
		},
	)

	require.NoError(err)
	assert.Equal(3, calls)
}

func TestWaitForWorkspaceReportsSetupFailureWithMessage(t *testing.T) {
	err := Poller{}.WaitForWorkspace(
		t.Context(), func(context.Context) (WorkspaceState, error) {
			return WorkspaceState{Status: "error", ErrorMessage: " clone failed "}, nil
		},
	)

	require.ErrorIs(t, err, ErrWorkspaceSetupFailed)
	assert.Equal(t, "workspace setup failed\nclone failed", err.Error())
}

func TestWaitForWorkspaceStopsOnDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	err := Poller{Interval: time.Millisecond}.WaitForWorkspace(
		ctx, func(context.Context) (WorkspaceState, error) {
			return WorkspaceState{Status: "creating"}, nil
		},
	)

	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestDeliverRetriesOnlyRetryableErrors(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	notReady := errors.New("input not ready")
	attempts := 0

	got, err := Deliver(
		t.Context(), Poller{Interval: time.Millisecond},
		func(context.Context) (string, error) {
			attempts++
			if attempts < 3 {
				return "pending", notReady
			}
			return "delivered", nil
		},
		func(err error) bool { return errors.Is(err, notReady) },
	)

	require.NoError(err)
	assert.Equal("delivered", got)
	assert.Equal(3, attempts)
}

func TestDeliverReturnsFinalErrorWithoutRetry(t *testing.T) {
	final := errors.New("write failed")
	attempts := 0

	got, err := Deliver(
		t.Context(), Poller{Interval: time.Hour},
		func(context.Context) (string, error) {
			attempts++
			return "uncertain", final
		},
		func(error) bool { return false },
	)

	require.ErrorIs(t, err, final)
	assert.Equal(t, "uncertain", got)
	assert.Equal(t, 1, attempts)
}

func TestDeliverKeepsLastResultWhenWaitEnds(t *testing.T) {
	notReady := errors.New("input not ready")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	got, err := Deliver(
		ctx, Poller{Interval: time.Millisecond},
		func(context.Context) (string, error) { return "pending", notReady },
		func(err error) bool { return errors.Is(err, notReady) },
	)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, "pending", got)
}
