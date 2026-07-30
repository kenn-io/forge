package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPollUntilDistinguishesDeadlineFromErrorAndCancel pins the contract
// install recovery relies on: only a clean deadline matches
// errPollDeadline (with the plain user-facing message), while probe
// errors and cancellation surface unchanged so adoption never runs for
// them.
func TestPollUntilDistinguishesDeadlineFromErrorAndCancel(t *testing.T) {
	t.Parallel()
	env := &appEnv{pollInterval: time.Millisecond}

	t.Run("deadline matches errPollDeadline with a clean message", func(t *testing.T) {
		t.Parallel()
		err := env.pollUntil(context.Background(), 20*time.Millisecond,
			func(context.Context) (bool, error) { return false, nil })
		require.ErrorIs(t, err, errPollDeadline)
		assert.Contains(t, err.Error(), "timed out after")
		assert.NotContains(t, err.Error(), "poll deadline reached")
	})

	t.Run("probe error surfaces and is not a deadline", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("probe boom")
		err := env.pollUntil(context.Background(), time.Second,
			func(context.Context) (bool, error) { return false, boom })
		require.ErrorIs(t, err, boom)
		assert.NotErrorIs(t, err, errPollDeadline)
	})

	t.Run("cancellation surfaces and is not a deadline", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		err := env.pollUntil(ctx, time.Second, func(context.Context) (bool, error) {
			cancel()
			return false, nil
		})
		require.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, errPollDeadline)
	})
}
