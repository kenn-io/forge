package msgvault

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// ErrMalformedUpstream marks a 2xx upstream response whose JSON shape cannot
// satisfy the route contract.
var ErrMalformedUpstream = errors.New("malformed upstream response")

// Error is the typed failure returned by every Client method when an upstream
// call returns a non-2xx status. Network and timeout errors surface as
// themselves; use Classify to route them.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("msgvault: %s", e.Message)
	}
	return fmt.Sprintf("msgvault: upstream %d %s: %s", e.Status, e.Code, e.Message)
}

// Classify reduces an arbitrary client error to a stable string code that the
// route layer maps to HTTP status and error envelope.
func Classify(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrMalformedUpstream) {
		return "malformed"
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "unauthorized"
		case http.StatusNotFound:
			return "not_found"
		case http.StatusTooManyRequests:
			return "rate_limited"
		default:
			return "upstream_error"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "down"
	}
	return "unknown"
}
