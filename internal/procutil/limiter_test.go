package procutil

import (
	"context"
	"errors"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimiterWaitsForReleaseWhenAtCapacity(t *testing.T) {
	require := require.New(t)

	limiter := NewLimiter(1)
	firstRelease, err := limiter.TryAcquire(context.Background(), "first subprocess")
	require.NoError(err)
	defer firstRelease()

	type acquireResult struct {
		release func()
		err     error
	}
	acquired := make(chan acquireResult, 1)
	go func() {
		release, acquireErr := limiter.TryAcquire(t.Context(), "second subprocess")
		acquired <- acquireResult{release: release, err: acquireErr}
	}()

	select {
	case got := <-acquired:
		require.NoError(got.err, "second acquire should wait for capacity instead of erroring")
		require.Fail("second acquire returned before capacity was released")
	case <-time.After(25 * time.Millisecond):
	}

	firstRelease()

	select {
	case got := <-acquired:
		require.NoError(got.err)
		require.NotNil(got.release)
		got.release()
	case <-time.After(time.Second):
		require.Fail("second acquire did not complete after capacity was released")
	}
}

func TestLimiterAcquireTimeoutIsResourceExhausted(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	limiter := NewLimiter(1)
	firstRelease, err := limiter.TryAcquire(context.Background(), "first subprocess")
	require.NoError(err)
	defer firstRelease()

	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()

	release, err := limiter.TryAcquire(ctx, "second subprocess")
	require.Error(err)
	require.Nil(release)
	assert.ErrorIs(err, ErrProcessLimitReached)
	assert.True(errors.Is(err, context.DeadlineExceeded))
	assert.True(IsResourceExhausted(err))
	assert.Contains(err.Error(), "second subprocess")
}
