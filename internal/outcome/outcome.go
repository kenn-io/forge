// Package outcome classifies failures for product event reporting.
package outcome

import (
	"context"
	"errors"
	"net"
	"net/http"

	gh "github.com/google/go-github/v92/github"
	bitbucketsdk "github.com/ktrysmt/go-bitbucket"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

// Kind classifies provider failures without exposing their messages.
func Kind(err error) string {
	status := 0
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
	case errors.Is(err, tokenauth.ErrMissingToken), errors.Is(err, platform.ErrMissingToken), errors.Is(err, platform.ErrPermissionDenied):
		return "auth"
	case errors.Is(err, platform.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, platform.ErrNotFound):
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
		if pe, ok := errors.AsType[*platform.Error](err); ok {
			switch pe.Code {
			case platform.ErrCodeConflict, platform.ErrCodeStaleState, platform.ErrCodeUnsupportedCapability, platform.ErrCodeUnsupportedMediaType, platform.ErrCodeRangeNotSatisfiable, platform.ErrCodeProviderNotConfigured, platform.ErrCodeInvalidRepoRef, platform.ErrCodeInvalidArgument:
				return "other"
			default:
				return "provider"
			}
		}
	}
	return "other"
}
