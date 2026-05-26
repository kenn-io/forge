package procutil

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimiterWaitsForReleaseWhenAtCapacity(t *testing.T) {
	require := require.New(t)

	type acquireResult struct {
		release func()
		err     error
	}

	synctest.Test(t, func(t *testing.T) {
		limiter := NewLimiter(1)
		firstRelease, err := limiter.TryAcquire(context.Background(), "first subprocess")
		require.NoError(err)
		defer firstRelease()

		acquired := make(chan acquireResult, 1)
		go func() {
			release, acquireErr := limiter.TryAcquire(
				context.Background(), "second subprocess",
			)
			acquired <- acquireResult{release: release, err: acquireErr}
		}()

		synctest.Wait()
		select {
		case got := <-acquired:
			require.NoError(got.err, "second acquire should wait for capacity instead of erroring")
			require.Fail("second acquire returned before capacity was released")
		default:
		}

		firstRelease()
		synctest.Wait()

		select {
		case got := <-acquired:
			require.NoError(got.err)
			require.NotNil(got.release)
			got.release()
		default:
			require.Fail("second acquire did not complete after capacity was released")
		}
	})
}

func TestLimiterAcquireTimeoutIsResourceExhausted(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	type acquireResult struct {
		release func()
		err     error
	}

	var got acquireResult
	synctest.Test(t, func(t *testing.T) {
		limiter := NewLimiterWithAcquireTimeout(1, 10*time.Second)
		firstRelease, err := limiter.TryAcquire(context.Background(), "first subprocess")
		require.NoError(err)
		defer firstRelease()

		acquired := make(chan acquireResult, 1)
		go func() {
			release, acquireErr := limiter.TryAcquire(
				context.Background(), "second subprocess",
			)
			acquired <- acquireResult{release: release, err: acquireErr}
		}()

		time.Sleep(10 * time.Second)
		synctest.Wait()
		select {
		case got = <-acquired:
		default:
			require.Fail("second acquire did not time out")
		}
	})

	require.Error(got.err)
	require.Nil(got.release)
	require.ErrorIs(got.err, ErrProcessLimitReached)
	require.ErrorIs(got.err, context.DeadlineExceeded)
	assert.True(IsResourceExhausted(got.err))
	assert.Contains(got.err.Error(), "second subprocess")
}

func TestLimiterAcquirePreservesCallerCancellation(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	limiter := NewLimiterWithAcquireTimeout(1, time.Second)
	firstRelease, err := limiter.TryAcquire(context.Background(), "first subprocess")
	require.NoError(err)
	defer firstRelease()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	release, err := limiter.TryAcquire(ctx, "second subprocess")
	require.Error(err)
	require.Nil(release)
	require.ErrorIs(err, ErrProcessLimitReached)
	require.ErrorIs(err, context.Canceled)
	assert.True(IsResourceExhausted(err))
}

func TestLimiterAcquireCanceledContextWithCapacityIsNotResourceExhausted(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	limiter := NewLimiterWithAcquireTimeout(1, time.Second)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	release, err := limiter.TryAcquire(ctx, "available subprocess")
	require.Error(err)
	require.Nil(release)
	require.ErrorIs(err, context.Canceled)
	require.NotErrorIs(err, ErrProcessLimitReached)
	assert.False(IsResourceExhausted(err))

	release, err = limiter.TryAcquire(context.Background(), "subsequent subprocess")
	require.NoError(err)
	require.NotNil(release)
	release()
}
