// Package server's problems.go defines the RFC 9457 (application/problem+json)
// error envelope that every internal/server/ failure path returns. The
// envelope adds two top-level fields beyond the huma defaults so frontend
// code can branch on stable, machine-readable signals instead of substring-
// matching English prose:
//
//   - `code`: a camelCase string from the closed enum below.
//   - `details`: a free-form map carrying machine-readable context.
//
// Construction goes through the problem* helpers; package init replaces
// huma.NewError so legacy huma.Error4xx/Error5xx callers (which we migrate
// away from in the same change set) still produce a valid envelope with
// a status-derived code while migration is in flight.
package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/platform"
)

// ProblemCode is the machine-readable error code carried on the wire.
type ProblemCode string

// The closed set of wire codes. Order matters: the enum struct tag on
// ProblemError.Code must list these in the same order, and the
// allProblemCodes test asserts the two stay in sync.
const (
	CodeBadRequest            ProblemCode = "badRequest"
	CodeBranchConflict        ProblemCode = "branchConflict"
	CodeCommentNotFound       ProblemCode = "commentNotFound"
	CodeConflict              ProblemCode = "conflict"
	CodeForbidden             ProblemCode = "forbidden"
	CodeInternalError         ProblemCode = "internalError"
	CodeIssueNotFound         ProblemCode = "issueNotFound"
	CodeNotFound              ProblemCode = "notFound"
	CodePayloadTooLarge       ProblemCode = "payloadTooLarge"
	CodeProjectNotFound       ProblemCode = "projectNotFound"
	CodePullNotFound          ProblemCode = "pullNotFound"
	CodeRateLimited           ProblemCode = "rateLimited"
	CodeRepoNotFound          ProblemCode = "repoNotFound"
	CodeServiceUnavailable    ProblemCode = "serviceUnavailable"
	CodeSettingsUnavailable   ProblemCode = "settingsUnavailable"
	CodeUnauthorized          ProblemCode = "unauthorized"
	CodeUnsupportedCapability ProblemCode = "unsupportedCapability"
	CodeUpstreamError         ProblemCode = "upstreamError"
	CodeValidationError       ProblemCode = "validationError"
	CodeWorkspaceNotFound     ProblemCode = "workspaceNotFound"
)

// allProblemCodes returns every declared ProblemCode in alphabetical order.
// The enum tag on ProblemError.Code must list the same set in the same
// order; problems_test.go asserts the two are consistent.
func allProblemCodes() []ProblemCode {
	return []ProblemCode{
		CodeBadRequest,
		CodeBranchConflict,
		CodeCommentNotFound,
		CodeConflict,
		CodeForbidden,
		CodeInternalError,
		CodeIssueNotFound,
		CodeNotFound,
		CodePayloadTooLarge,
		CodeProjectNotFound,
		CodePullNotFound,
		CodeRateLimited,
		CodeRepoNotFound,
		CodeServiceUnavailable,
		CodeSettingsUnavailable,
		CodeUnauthorized,
		CodeUnsupportedCapability,
		CodeUpstreamError,
		CodeValidationError,
		CodeWorkspaceNotFound,
	}
}

// ProblemError is the RFC 9457 problem-details envelope returned for every
// failure path. The huma.ErrorModel-compatible fields (Type/Title/Status/
// Detail/Instance/Errors) keep behavior parity with huma's defaults so
// existing clients keep working; the Code and Details extension members
// are new.
//
// The Go type name "ProblemError" intentionally differs from huma's
// "ErrorModel" to avoid shadowing the upstream type. The OpenAPI schema
// name therefore becomes ProblemError too, and generated clients pick
// up that symbol (components["schemas"]["ProblemError"] in TS,
// apiclient.ProblemError in Go).
type ProblemError struct {
	// Type is a URI reference identifying the problem type. Defaults to
	// "about:blank" per RFC 9457 when not set.
	Type string `json:"type,omitempty" format:"uri" default:"about:blank" example:"https://example.com/errors/example" doc:"A URI reference to human-readable documentation for the error."`

	// Title is a short, human-readable summary. Stable across occurrences
	// of the same problem type.
	Title string `json:"title,omitempty" example:"Bad Request" doc:"A short, human-readable summary of the problem type. This value should not change between occurrences of the error."`

	// Status is the HTTP status code. Always set by the helpers.
	Status int `json:"status,omitempty" example:"400" doc:"HTTP status code"`

	// Detail is a human-readable explanation of this occurrence.
	Detail string `json:"detail,omitempty" example:"Property foo is required but is missing." doc:"A human-readable explanation specific to this occurrence of the problem."`

	// Instance is a URI reference identifying this specific occurrence.
	Instance string `json:"instance,omitempty" format:"uri" example:"https://example.com/error-log/abc123" doc:"A URI reference that identifies the specific occurrence of the problem."`

	// Errors keeps parity with huma.ErrorModel for the validation-failure
	// path where huma emits per-field details. New code paths should
	// populate Details instead and leave Errors empty.
	Errors []*huma.ErrorDetail `json:"errors,omitempty" doc:"Optional list of individual error details"`

	// Code is the machine-readable error code drawn from the closed enum
	// in allProblemCodes(). Frontend logic branches on this value.
	Code ProblemCode `json:"code" enum:"badRequest,branchConflict,commentNotFound,conflict,forbidden,internalError,issueNotFound,notFound,payloadTooLarge,projectNotFound,pullNotFound,rateLimited,repoNotFound,serviceUnavailable,settingsUnavailable,unauthorized,unsupportedCapability,upstreamError,validationError,workspaceNotFound" example:"badRequest" doc:"Machine-readable error code. Stable across occurrences."`

	// Details is a free-form map of machine-readable context for this
	// occurrence (e.g. {capability: "merge_mutation"} or
	// {retryAfter: "2026-05-19T12:00:00Z"}).
	Details map[string]any `json:"details,omitempty" doc:"Machine-readable error context, keyed by code-specific conventions."`
}

// Error returns Detail (or the code if Detail is empty) so ProblemError
// satisfies the error interface.
func (p *ProblemError) Error() string {
	if p == nil {
		return "<nil problem>"
	}
	if p.Detail != "" {
		return p.Detail
	}
	return string(p.Code)
}

// GetStatus satisfies huma.StatusError.
func (p *ProblemError) GetStatus() int { return p.Status }

// ContentType rewrites application/json to application/problem+json per
// RFC 9457, mirroring huma's ErrorModel behavior.
func (p *ProblemError) ContentType(ct string) string {
	switch ct {
	case "application/json":
		return "application/problem+json"
	case "application/cbor":
		return "application/problem+cbor"
	default:
		return ct
	}
}

// codeForStatus maps an HTTP status to the default wire code. Used when
// callers reach for huma.Error4xx without specifying a code (e.g. during
// migration) or when our own helpers don't pick a richer code.
func codeForStatus(status int) ProblemCode {
	switch status {
	case http.StatusBadRequest:
		return CodeBadRequest
	case http.StatusUnauthorized:
		return CodeUnauthorized
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeConflict
	case http.StatusRequestEntityTooLarge:
		return CodePayloadTooLarge
	case http.StatusUnprocessableEntity:
		return CodeValidationError
	case http.StatusTooManyRequests:
		return CodeRateLimited
	case http.StatusServiceUnavailable:
		return CodeServiceUnavailable
	case http.StatusBadGateway:
		return CodeUpstreamError
	default:
		if status >= 500 {
			return CodeInternalError
		}
		return CodeBadRequest
	}
}

// titleForStatus returns the standard HTTP status text for a status code,
// matching the huma.ErrorModel default behavior.
func titleForStatus(status int) string {
	if t := http.StatusText(status); t != "" {
		return t
	}
	return "Error"
}

// newProblem is the canonical constructor. Status drives both the wire
// status and (when code is empty) the default code; detail is the
// human-readable message; details is the machine-readable context. The
// returned value satisfies huma.StatusError.
func newProblem(status int, code ProblemCode, detail string, details map[string]any) *ProblemError {
	if code == "" {
		code = codeForStatus(status)
	}
	return &ProblemError{
		Status:  status,
		Title:   titleForStatus(status),
		Detail:  detail,
		Code:    code,
		Details: details,
	}
}

// problemBadRequest returns a 400 with the supplied code (defaults to
// CodeBadRequest when "").
func problemBadRequest(code ProblemCode, detail string, details map[string]any) huma.StatusError {
	if code == "" {
		code = CodeBadRequest
	}
	return newProblem(http.StatusBadRequest, code, detail, details)
}

// problemValidation returns a 400 with code CodeValidationError, embedding
// the offending field and (optionally) the allowed values. Field is the
// JSON path of the value at fault ("body.status", "query.repo", etc.).
func problemValidation(field, detail string, allowed ...string) huma.StatusError {
	d := map[string]any{}
	if field != "" {
		d["field"] = field
	}
	if len(allowed) > 0 {
		d["allowed"] = allowed
	}
	if len(d) == 0 {
		d = nil
	}
	return newProblem(http.StatusBadRequest, CodeValidationError, detail, d)
}

// problemNotFound returns a 404 with the supplied code. Pass CodeNotFound
// for the generic case or one of CodeRepoNotFound, CodePullNotFound, etc.
// for richer semantics.
func problemNotFound(code ProblemCode, detail string, details map[string]any) huma.StatusError {
	if code == "" {
		code = CodeNotFound
	}
	return newProblem(http.StatusNotFound, code, detail, details)
}

// problemConflict returns a 409 with the supplied code.
func problemConflict(code ProblemCode, detail string, details map[string]any) huma.StatusError {
	if code == "" {
		code = CodeConflict
	}
	return newProblem(http.StatusConflict, code, detail, details)
}

// problemForbidden returns a 403.
func problemForbidden(detail string, details map[string]any) huma.StatusError {
	return newProblem(http.StatusForbidden, CodeForbidden, detail, details)
}

// problemInternal returns a 500.
func problemInternal(detail string) huma.StatusError {
	return newProblem(http.StatusInternalServerError, CodeInternalError, detail, nil)
}

// problemUpstream returns a 502 (provider API failure). The optional
// provider/host are surfaced in details when non-empty.
func problemUpstream(detail, provider, host string) huma.StatusError {
	d := map[string]any{}
	if provider != "" {
		d["provider"] = provider
	}
	if host != "" {
		d["platformHost"] = host
	}
	if len(d) == 0 {
		d = nil
	}
	return newProblem(http.StatusBadGateway, CodeUpstreamError, detail, d)
}

// problemServiceUnavailable returns a 503.
func problemServiceUnavailable(detail string) huma.StatusError {
	return newProblem(http.StatusServiceUnavailable, CodeServiceUnavailable, detail, nil)
}

// problemPayloadTooLarge returns a 413 with maxBytes in details when known.
func problemPayloadTooLarge(detail string, maxBytes int64) huma.StatusError {
	var d map[string]any
	if maxBytes > 0 {
		d = map[string]any{"maxBytes": maxBytes}
	}
	return newProblem(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, detail, d)
}

// problemUnsupportedCapability returns a 409 with code
// CodeUnsupportedCapability and details {capability, provider,
// platformHost}.
func problemUnsupportedCapability(repo db.Repo, capability string) huma.StatusError {
	details := map[string]any{
		"capability":   capability,
		"provider":     string(repoProviderKind(repo)),
		"platformHost": repoProviderHost(repo),
	}
	return newProblem(
		http.StatusConflict,
		CodeUnsupportedCapability,
		"Unsupported provider capability",
		details,
	)
}

// problemRateLimited returns a 429 with code CodeRateLimited. retryAfter
// is rendered as an RFC 3339 string when non-nil; provider/host go into
// details when non-empty.
func problemRateLimited(provider, host string, retryAfter *time.Time) huma.StatusError {
	d := map[string]any{}
	if retryAfter != nil {
		d["retryAfter"] = retryAfter.UTC().Format(time.RFC3339)
	}
	if provider != "" {
		d["provider"] = provider
	}
	if host != "" {
		d["platformHost"] = host
	}
	if len(d) == 0 {
		d = nil
	}
	return newProblem(
		http.StatusTooManyRequests,
		CodeRateLimited,
		"Upstream rate limit exceeded",
		d,
	)
}

// problemBranchConflict returns the 409 used when a local branch already
// exists with the workspace's requested name. The branch and suggested
// alternative go into details.
func problemBranchConflict(branch, suggested string) huma.StatusError {
	d := map[string]any{}
	if branch != "" {
		d["branch"] = branch
	}
	if suggested != "" {
		d["suggestedBranch"] = suggested
	}
	if len(d) == 0 {
		d = nil
	}
	return newProblem(
		http.StatusConflict,
		CodeBranchConflict,
		"A local branch with the requested name already exists.",
		d,
	)
}

// providerCallProblem translates a provider-call error into a wire
// problem. Provider/host narrow the upstream problem when no platform.Error
// is in the chain; when one is, mapPlatformError handles the translation
// (and ignores the provider/host arguments).
func providerCallProblem(err error, provider, host string) huma.StatusError {
	return providerCallProblemWithDetail(err, provider, host, "")
}

func providerCallProblemWithDetail(
	err error,
	provider, host, detail string,
) huma.StatusError {
	if err == nil {
		return nil
	}
	var pe *platform.Error
	if errors.As(err, &pe) {
		if mapped := mapPlatformError(err); mapped != nil {
			return mapped
		}
	}
	if detail == "" {
		detail = err.Error()
	}
	return problemUpstream(detail, provider, host)
}

// mapPlatformError translates an error from internal/platform into a wire
// problem. Returns nil for nil input or context cancellation so callers
// can propagate those without altering control flow. Returns a generic
// upstream problem with the error's text when no platform.Error is in
// the chain.
func mapPlatformError(err error) huma.StatusError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	var pe *platform.Error
	if !errors.As(err, &pe) {
		return problemUpstream(err.Error(), "", "")
	}
	provider := string(pe.Provider)
	host := pe.PlatformHost
	switch pe.Code {
	case platform.ErrCodeUnsupportedCapability:
		// We don't have a db.Repo here, so synthesize the details directly
		// rather than going through problemUnsupportedCapability.
		details := map[string]any{
			"capability":   pe.Capability,
			"provider":     provider,
			"platformHost": host,
		}
		return newProblem(
			http.StatusConflict,
			CodeUnsupportedCapability,
			"Unsupported provider capability",
			details,
		)
	case platform.ErrCodeRateLimited:
		return problemRateLimited(provider, host, pe.ResetAt)
	case platform.ErrCodePermissionDenied:
		d := map[string]any{}
		if provider != "" {
			d["provider"] = provider
		}
		if host != "" {
			d["platformHost"] = host
		}
		if len(d) == 0 {
			d = nil
		}
		return problemForbidden(err.Error(), d)
	case platform.ErrCodeNotFound:
		return problemNotFound(CodeNotFound, err.Error(), nil)
	case platform.ErrCodeProviderNotConfigured,
		platform.ErrCodeMissingToken,
		platform.ErrCodeInvalidRepoRef:
		return problemBadRequest(CodeBadRequest, err.Error(), nil)
	default:
		return problemUpstream(err.Error(), provider, host)
	}
}

// init replaces huma.NewError so any remaining huma.ErrorNxx callers
// (or huma's own internal validators) emit a ProblemError envelope.
// Without this hook, migration would be all-or-nothing: any missed call
// site would emit a body with no code field. With it, every code path
// already produces a code (status-derived) and migration becomes
// incremental.
func init() {
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		details := make([]*huma.ErrorDetail, 0, len(errs))
		for _, e := range errs {
			if e == nil {
				continue
			}
			if d, ok := e.(huma.ErrorDetailer); ok {
				details = append(details, d.ErrorDetail())
				continue
			}
			details = append(details, &huma.ErrorDetail{Message: e.Error()})
		}
		p := newProblem(status, codeForStatus(status), msg, nil)
		if len(details) > 0 {
			p.Errors = details
		}
		return p
	}
}
