package gitclone

import (
	"context"
	"strings"

	"github.com/cenkalti/backoff/v7"
	internalretry "go.kenn.io/forge/internal/retry"
)

// retryAttempts caps how many times a transient git smart-HTTP failure is
// re-issued before surfacing the error to the caller.
const retryAttempts = internalretry.DefaultMaxTries

type gitErrorPattern struct {
	text  string
	retry bool
}

var serverGitErrorSubstrings = []gitErrorPattern{
	{"internal server error", true},
	{"returned error: 500", true},
	{"returned error: 502", true},
	{"returned error: 503", true},
	{"returned error: 504", true},
	// Other 5xx failures count as provider outages without changing retries.
	{"returned error: 5", false},
}

var networkGitErrorSubstrings = []gitErrorPattern{
	{"connection reset", true},
	{"connection refused", true},
	{"could not resolve host", true},
	{"operation timed out", true},
	{"operation too slow", true},
	{"early eof", true},
	{"ssl_read", true},
	{"tls connection", true},
}

var authGitErrorSubstrings = []gitErrorPattern{
	{"authentication failed", true},
	{"could not read username", true},
	{"terminal prompts disabled", true},
	{"returned error: 401", false},
	{"returned error: 403", false},
}

var rateLimitGitErrorSubstrings = []gitErrorPattern{
	{"returned error: 429", false},
}

// IsTransientGitError reports whether the stderr captured from git looks
// like a transient remote or network failure worth retrying. GitHub's
// smart-HTTP endpoint sporadically returns 5xx on /info/refs even when the
// repo is healthy; retrying inside the same sync cycle usually succeeds and
// avoids dropping a full sync window.
func IsTransientGitError(err error) bool {
	return matchesGitError(err, serverGitErrorSubstrings, true) || matchesGitError(err, networkGitErrorSubstrings, true)
}

// IsServerGitError reports whether git stderr indicates an HTTP server failure.
func IsServerGitError(err error) bool {
	return matchesGitError(err, serverGitErrorSubstrings, false)
}

// IsNetworkGitError reports whether git stderr indicates a network failure.
func IsNetworkGitError(err error) bool {
	return matchesGitError(err, networkGitErrorSubstrings, false)
}

// IsRateLimitedGitError reports whether git stderr indicates HTTP rate limiting.
func IsRateLimitedGitError(err error) bool {
	return matchesGitError(err, rateLimitGitErrorSubstrings, false)
}

func isAuthGitError(err error) bool {
	return matchesGitError(err, authGitErrorSubstrings, true)
}

func matchesGitError(err error, patterns []gitErrorPattern, retryOnly bool) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, pattern := range patterns {
		if (!retryOnly || pattern.retry) && strings.Contains(msg, pattern.text) {
			return true
		}
	}
	return false
}

// retryTransient runs op with bounded exponential backoff, retrying only
// when the underlying error matches IsTransientGitError. Permanent errors
// (auth, not-found, malformed remote, etc.) short-circuit immediately so
// they surface to the caller without delay.
func retryTransient[T any](
	ctx context.Context, label string, op func() (T, error),
) (T, error) {
	return internalretry.Do(ctx, internalretry.Config[T]{
		Label:       label,
		MaxTries:    retryAttempts,
		IsTransient: IsTransientGitError,
		Op:          op,
	})
}

// retryTransientWithBackOff is the seam used by tests to inject a faster
// schedule; production callers should use retryTransient.
func retryTransientWithBackOff[T any](
	ctx context.Context,
	label string,
	bo backoff.BackOff,
	op func() (T, error),
) (T, error) {
	return internalretry.DoWithBackOff(ctx, internalretry.Config[T]{
		Label:       label,
		BackOff:     bo,
		MaxTries:    retryAttempts,
		IsTransient: IsTransientGitError,
		Op:          op,
	})
}
