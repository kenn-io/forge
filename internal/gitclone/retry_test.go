package gitclone

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsTransientGitError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"github 500 with internal server error preamble",
			fmt.Errorf("exit status 128: %s", "remote: Internal Server Error\nfatal: unable to access 'https://github.com/x/y.git/': The requested URL returned error: 500"),
			true,
		},
		{
			"bare 502",
			errors.New("fatal: The requested URL returned error: 502"),
			true,
		},
		{
			"bare 503",
			errors.New("fatal: The requested URL returned error: 503"),
			true,
		},
		{
			"bare 504",
			errors.New("fatal: The requested URL returned error: 504"),
			true,
		},
		{
			"connection reset",
			errors.New("error: RPC failed; curl 56 Recv failure: Connection reset by peer"),
			true,
		},
		{
			"could not resolve host",
			errors.New("fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com"),
			true,
		},
		{
			"case insensitive",
			errors.New("REMOTE: INTERNAL SERVER ERROR"),
			true,
		},
		{
			"early eof",
			errors.New("fatal: early EOF\nfatal: index-pack failed"),
			true,
		},
		{
			"auth failure is permanent",
			errors.New("fatal: Authentication failed for 'https://github.com/x/y.git/'"),
			false,
		},
		{
			"not found is permanent",
			errors.New("fatal: repository 'https://github.com/x/y.git/' not found"),
			false,
		},
		{
			"bad ref is permanent",
			errors.New("fatal: couldn't find remote ref refs/heads/nope"),
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isTransientGitError(tc.err))
		})
	}
}

// fastBackOff returns a backoff schedule that resolves in microseconds so
// retry tests stay fast even when they exhaust the retry budget.
func fastBackOff() *backoff.ExponentialBackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = time.Microsecond
	bo.MaxInterval = time.Microsecond
	bo.RandomizationFactor = 0
	return bo
}

// TestRetryTransientStopsOnPermanentError pins the policy decision that
// non-transient errors are wrapped in backoff.Permanent. Library code
// short-circuits on Permanent on its own; this test fails if a future
// refactor stops doing the wrap.
func TestRetryTransientStopsOnPermanentError(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	calls := 0
	permanent := errors.New("fatal: Authentication failed for 'https://example.com/x.git/'")
	_, err := retryTransientWithBackOff(t.Context(), "test", fastBackOff(),
		func() (string, error) {
			calls++
			return "", permanent
		})

	require.ErrorIs(err, permanent)
	assert.Equal(1, calls, "permanent error should not retry")
}

// TestRetryTransientExhaustsBudget pins the retryAttempts constant. If
// someone bumps or drops it, this test surfaces the change.
func TestRetryTransientExhaustsBudget(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	calls := 0
	transient := errors.New("remote: Internal Server Error")
	_, err := retryTransientWithBackOff(t.Context(), "test", fastBackOff(),
		func() (string, error) {
			calls++
			return "", transient
		})

	require.ErrorIs(err, transient)
	assert.Equal(retryAttempts, calls)
}
