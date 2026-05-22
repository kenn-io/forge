package gitclone

import (
	"context"
	"strings"

	"github.com/cenkalti/backoff/v5"
	internalretry "go.kenn.io/middleman/internal/retry"
)

// retryAttempts caps how many times a transient git smart-HTTP failure is
// re-issued before surfacing the error to the caller.
const retryAttempts = internalretry.DefaultMaxTries

var transientGitErrorSubstrings = []string{
	"internal server error",
	"returned error: 500",
	"returned error: 502",
	"returned error: 503",
	"returned error: 504",
	"connection reset",
	"connection refused",
	"could not resolve host",
	"operation timed out",
	"early eof",
	"ssl_read",
	"tls connection",
}

// isTransientGitError reports whether the stderr captured from git looks
// like a transient remote or network failure worth retrying. GitHub's
// smart-HTTP endpoint sporadically returns 5xx on /info/refs even when the
// repo is healthy; retrying inside the same sync cycle usually succeeds and
// avoids dropping a full sync window.
func isTransientGitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range transientGitErrorSubstrings {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// retryTransient runs op with bounded exponential backoff, retrying only
// when the underlying error matches isTransientGitError. Permanent errors
// (auth, not-found, malformed remote, etc.) short-circuit immediately so
// they surface to the caller without delay.
func retryTransient[T any](
	ctx context.Context, label string, op func() (T, error),
) (T, error) {
	return internalretry.Do(ctx, internalretry.Config[T]{
		Label:       label,
		MaxTries:    retryAttempts,
		IsTransient: isTransientGitError,
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
		IsTransient: isTransientGitError,
		Op:          op,
	})
}
