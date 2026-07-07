package retry

import (
	"errors"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fastBackOff() *backoff.ExponentialBackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = time.Microsecond
	bo.MaxInterval = time.Microsecond
	bo.RandomizationFactor = 0
	return bo
}

func TestDoWithBackOffStopsOnPermanentError(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	calls := 0
	permanent := errors.New("permanent")

	_, err := DoWithBackOff(t.Context(), Config[string]{
		Label:       "test",
		BackOff:     fastBackOff(),
		MaxTries:    3,
		IsTransient: func(err error) bool { return false },
		Op: func() (string, error) {
			calls++
			return "", permanent
		},
	})

	require.ErrorIs(err, permanent)
	assert.Equal(1, calls)
}

func TestDoWithBackOffRetriesTransientErrorUntilBudgetExhausted(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	calls := 0
	transient := errors.New("transient")

	_, err := DoWithBackOff(t.Context(), Config[string]{
		Label:       "test",
		BackOff:     fastBackOff(),
		MaxTries:    3,
		IsTransient: func(err error) bool { return true },
		Op: func() (string, error) {
			calls++
			return "", transient
		},
	})

	require.ErrorIs(err, transient)
	assert.Equal(3, calls)
}

func TestDoWithBackOffRetriesTransientErrorUntilSuccess(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	calls := 0

	got, err := DoWithBackOff(t.Context(), Config[string]{
		Label:       "test",
		BackOff:     fastBackOff(),
		MaxTries:    3,
		IsTransient: func(err error) bool { return true },
		Op: func() (string, error) {
			calls++
			if calls < 3 {
				return "", errors.New("transient")
			}
			return "ok", nil
		},
	})

	require.NoError(err)
	assert.Equal("ok", got)
	assert.Equal(3, calls)
}
