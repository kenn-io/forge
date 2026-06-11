package platform

import (
	"errors"
	"fmt"
	"time"
)

type PlatformErrorCode string

const (
	ErrCodeUnsupportedCapability PlatformErrorCode = "unsupported_capability"
	ErrCodeProviderNotConfigured PlatformErrorCode = "provider_not_configured"
	ErrCodeMissingToken          PlatformErrorCode = "missing_token"
	ErrCodeInvalidRepoRef        PlatformErrorCode = "invalid_repo_ref"
	ErrCodeInvalidArgument       PlatformErrorCode = "invalid_argument"
	ErrCodePermissionDenied      PlatformErrorCode = "permission_denied"
	ErrCodeNotFound              PlatformErrorCode = "not_found"
	ErrCodeRateLimited           PlatformErrorCode = "rate_limited"
)

var (
	ErrUnsupportedCapability = &Error{Code: ErrCodeUnsupportedCapability}
	ErrProviderNotConfigured = &Error{Code: ErrCodeProviderNotConfigured}
	ErrMissingToken          = &Error{Code: ErrCodeMissingToken}
	ErrInvalidRepoRef        = &Error{Code: ErrCodeInvalidRepoRef}
	ErrInvalidArgument       = &Error{Code: ErrCodeInvalidArgument}
	ErrPermissionDenied      = &Error{Code: ErrCodePermissionDenied}
	ErrNotFound              = &Error{Code: ErrCodeNotFound}
	ErrRateLimited           = &Error{Code: ErrCodeRateLimited}
)

type Error struct {
	Code         PlatformErrorCode
	Provider     Kind
	PlatformHost string
	Capability   string
	TokenEnv     string
	Field        string
	ResetAt      *time.Time
	Err          error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	message := string(e.Code)
	if e.Provider != "" || e.PlatformHost != "" {
		message = fmt.Sprintf("%s for %s/%s", message, e.Provider, e.PlatformHost)
	}
	if e.Capability != "" {
		message = fmt.Sprintf("%s: %s", message, e.Capability)
	}
	if e.Err != nil {
		message = fmt.Sprintf("%s: %v", message, e.Err)
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *Error) Is(target error) bool {
	var targetErr *Error
	if !errors.As(target, &targetErr) {
		return false
	}
	return e != nil && e.Code == targetErr.Code
}

func ProviderNotConfigured(kind Kind, host string) error {
	return &Error{
		Code:         ErrCodeProviderNotConfigured,
		Provider:     kind,
		PlatformHost: host,
	}
}

func UnsupportedCapability(kind Kind, host, capability string) error {
	return &Error{
		Code:         ErrCodeUnsupportedCapability,
		Provider:     kind,
		PlatformHost: host,
		Capability:   capability,
	}
}
