package platform

import (
	"errors"
	"fmt"
	"time"
)

type PlatformErrorCode string

type MutationOutcome string

const (
	MutationOutcomeUnknown            MutationOutcome = ""
	MutationOutcomeDefinitelyRejected MutationOutcome = "definitely_rejected"
)

const (
	ErrCodeUnsupportedCapability PlatformErrorCode = "unsupported_capability"
	ErrCodeProviderNotConfigured PlatformErrorCode = "provider_not_configured"
	ErrCodeMissingToken          PlatformErrorCode = "missing_token"
	ErrCodeInvalidRepoRef        PlatformErrorCode = "invalid_repo_ref"
	ErrCodeInvalidArgument       PlatformErrorCode = "invalid_argument"
	ErrCodePermissionDenied      PlatformErrorCode = "permission_denied"
	ErrCodeNotFound              PlatformErrorCode = "not_found"
	ErrCodeRateLimited           PlatformErrorCode = "rate_limited"
	// ErrCodeStaleState marks mutations rejected because the target moved
	// past the state the caller acted on (for example an MR head SHA that
	// advanced after review).
	ErrCodeStaleState PlatformErrorCode = "stale_state"
	// ErrCodeConflict marks provider conflicts that are not staleness:
	// the request was understood but the target's current state refuses
	// it (for example merging an unmergeable MR).
	ErrCodeConflict PlatformErrorCode = "conflict"
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
	ErrStaleState            = &Error{Code: ErrCodeStaleState}
	ErrConflict              = &Error{Code: ErrCodeConflict}
)

// MutationDefinitelyRejected reports only error categories whose contract
// guarantees that the provider did not apply the requested mutation. Unknown
// and future categories remain ambiguous and must retain any durable intent.
func MutationDefinitelyRejected(err error) bool {
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr == nil {
		return false
	}
	return providerErr.MutationOutcome == MutationOutcomeDefinitelyRejected
}

type Error struct {
	Code         PlatformErrorCode
	Provider     Kind
	PlatformHost string
	Capability   string
	TokenEnv     string
	Field        string
	ResetAt      *time.Time
	// Hint carries client-safe side-effect context that must survive
	// problem mapping — e.g. an approval that could not be revoked
	// after the head moved, or a review note that was already posted
	// before the failure. Err keeps the full chain for logs; Hint is
	// what an API client needs to act on the side effect.
	Hint string
	// Details carries stable, client-safe extension members merged
	// into the problem details map (e.g. revocation: "failed",
	// review_id: "31") so clients can branch on side-effect outcomes
	// without parsing Hint prose. Keys must not collide with the
	// reserved problem members (reason, provider, platformHost).
	Details map[string]string
	// MutationOutcome is set only by mutation adapters at a point where they
	// can prove whether the provider performed the requested side effect.
	MutationOutcome MutationOutcome
	Err             error
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
