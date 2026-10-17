package platform

import (
	"context"
	"errors"
	"net"
	"net/http"

	gh "github.com/google/go-github/v92/github"
	bitbucketsdk "github.com/ktrysmt/go-bitbucket"
)

// OutcomeKind classifies provider failures without exposing their messages.
// Preserve status from raw SDK and adapter errors at every reporting boundary.
func OutcomeKind(err error, status int) string {
	if err == nil {
		return ""
	}
	if _, ok := errors.AsType[*gh.RateLimitError](err); ok {
		return "rate_limited"
	}
	if _, ok := errors.AsType[*gh.AbuseRateLimitError](err); ok {
		return "rate_limited"
	}
	if response, ok := errors.AsType[*gh.ErrorResponse](err); ok && response.Response != nil {
		status = response.Response.StatusCode
	}
	if response, ok := errors.AsType[*bitbucketsdk.UnexpectedResponseStatusError](err); ok {
		status = response.StatusCode
	}
	if response, ok := errors.AsType[interface {
		error
		HTTPStatus() int
	}](err); ok {
		status = response.HTTPStatus()
	}
	switch {
	case errors.Is(err, ErrMissingToken), errors.Is(err, ErrPermissionDenied):
		return "auth"
	case errors.Is(err, ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "auth"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusTooManyRequests:
		return "rate_limited"
	}
	if status >= 500 && status < 600 {
		return "provider"
	}
	if _, ok := errors.AsType[net.Error](err); ok || errors.Is(err, context.DeadlineExceeded) {
		return "network"
	}
	if status == 0 {
		if pe, ok := errors.AsType[*Error](err); ok {
			switch pe.Code {
			case ErrCodeConflict, ErrCodeStaleState, ErrCodeUnsupportedCapability, ErrCodeUnsupportedMediaType, ErrCodeRangeNotSatisfiable, ErrCodeProviderNotConfigured, ErrCodeInvalidRepoRef, ErrCodeInvalidArgument:
				return "other"
			default:
				return "provider"
			}
		}
	}
	return "other"
}
