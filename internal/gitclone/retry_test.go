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
		name        string
		err         error
		want        bool
		wantServer  bool
		wantNetwork bool
	}{
		{"nil", nil, false, false, false},
		{"git refused", errors.New("fatal: unable to access remote: Connection refused"), true, false, true},
		{"git timeout", errors.New("fatal: unable to access remote: Operation timed out"), true, false, true},
		{
			"github 500 with internal server error preamble",
			fmt.Errorf("exit status 128: %s", "remote: Internal Server Error\nfatal: unable to access 'https://github.com/x/y.git/': The requested URL returned error: 500"),
			true, true, false,
		},
		{
			"bare 502",
			errors.New("fatal: The requested URL returned error: 502"),
			true, true, false,
		},
		{
			"bare 503",
			errors.New("fatal: The requested URL returned error: 503"),
			true, true, false,
		},
		{
			"bare 504",
			errors.New("fatal: The requested URL returned error: 504"),
			true, true, false,
		},
		{"HTTP 401 is permanent", errors.New("fatal: The requested URL returned error: 401"), false, false, false},
		{"HTTP 403 is permanent", errors.New("fatal: The requested URL returned error: 403"), false, false, false},
		{"HTTP 429 is permanent", errors.New("fatal: The requested URL returned error: 429"), false, false, false},
		{"HTTP 501 is permanent", errors.New("fatal: The requested URL returned error: 501"), false, true, false},
		{"HTTP 599 is permanent", errors.New("fatal: The requested URL returned error: 599"), false, true, false},
		{
			"connection reset",
			errors.New("error: RPC failed; curl 56 Recv failure: Connection reset by peer"),
			true, false, true,
		},
		{
			"stalled HTTP transfer",
			errors.New("fatal: unable to access 'https://example.com/repo.git/': Operation too slow. Less than 1 bytes/sec transferred the last 30 seconds"),
			true, false, true,
		},
		{
			"could not resolve host",
			errors.New("fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com"),
			true, false, true,
		},
		{
			"case insensitive",
			errors.New("REMOTE: INTERNAL SERVER ERROR"),
			true, true, false,
		},
		{
			"early eof",
			errors.New("fatal: early EOF\nfatal: index-pack failed"),
			true, false, true,
		},
		{
			"auth failure is permanent",
			errors.New("fatal: Authentication failed for 'https://github.com/x/y.git/'"),
			false, false, false,
		},
		{
			"not found is permanent",
			errors.New("fatal: repository 'https://github.com/x/y.git/' not found"),
			false, false, false,
		},
		{
			"bad ref is permanent",
			errors.New("fatal: couldn't find remote ref refs/heads/nope"),
			false, false, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsTransientGitError(tc.err))
			assert.Equal(t, tc.wantServer, IsServerGitError(tc.err))
			assert.Equal(t, tc.wantNetwork, IsNetworkGitError(tc.err))
		})
	}
}

func TestGitHTTPAuthentication(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			err := fmt.Errorf("fatal: The requested URL returned error: %d", status)
			assert.False(t, IsAuthenticationFailure(err), "HTTP status alone must leave Git access unchanged")
			assert.False(t, isAuthGitError(err), "HTTP status alone must not trigger a credential retry")
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
	transient := errors.New("fatal: Operation too slow. Less than 1 bytes/sec transferred the last 30 seconds")
	_, err := retryTransientWithBackOff(t.Context(), "test", fastBackOff(),
		func() (string, error) {
			calls++
			return "", transient
		})

	require.ErrorIs(err, transient)
	assert.Equal(retryAttempts, calls)
}
