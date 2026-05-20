package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/platform"
)

// structField is a tiny reflection helper for inspecting struct tags
// from tests. Returns the named field on the supplied pointer's element
// type. The bool is false when the field doesn't exist.
func structField(p any, name string) (reflect.StructField, bool) {
	t := reflect.TypeOf(p)
	if t == nil {
		return reflect.StructField{}, false
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	return t.FieldByName(name)
}

// TestProblemErrorEnumTagMatchesConstants asserts that the `enum:` struct
// tag on ProblemError.Code lists exactly the same wire values that
// allProblemCodes() returns, in the same order. If a new code is added
// to one list and the other is forgotten, this test catches the drift.
func TestProblemErrorEnumTagMatchesConstants(t *testing.T) {
	require := require.New(t)

	// Pull the enum tag off ProblemError.Code via reflection.
	codeField, ok := structField((*ProblemError)(nil), "Code")
	require.True(ok, "Code field not found on ProblemError")

	enumTag, ok := codeField.Tag.Lookup("enum")
	require.True(ok, "enum tag missing on Code field")
	tagValues := strings.Split(enumTag, ",")

	declared := allProblemCodes()
	require.Len(tagValues, len(declared), "enum tag and allProblemCodes() length differ")

	for i, c := range declared {
		require.Equal(string(c), tagValues[i], "enum tag entry %d differs from declared code", i)
	}

	// And the slice should be sorted alphabetically to keep OpenAPI diffs
	// stable as new codes are added.
	sorted := make([]string, len(declared))
	for i, c := range declared {
		sorted[i] = string(c)
	}
	require.True(sort.StringsAreSorted(sorted), "allProblemCodes() must be alphabetical")
}

func TestProblemErrorContentTypeRewritesJSON(t *testing.T) {
	assert := Assert.New(t)
	p := &ProblemError{Status: http.StatusBadRequest, Code: CodeBadRequest}

	assert.Equal("application/problem+json", p.ContentType("application/json"))
	assert.Equal("application/problem+cbor", p.ContentType("application/cbor"))
	assert.Equal("text/plain", p.ContentType("text/plain"))
}

func TestProblemErrorRoundTripJSONPreservesCodeAndDetails(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	original := newProblem(
		http.StatusConflict,
		CodeUnsupportedCapability,
		"Unsupported provider capability",
		map[string]any{
			"capability":   "merge_mutation",
			"provider":     "gitlab",
			"platformHost": "gitlab.example.com",
		},
	)

	encoded, err := json.Marshal(original)
	require.NoError(err)

	// Top-level code and details survive the round trip.
	require.Contains(string(encoded), `"code":"unsupportedCapability"`)
	require.Contains(string(encoded), `"details":{`)

	var decoded ProblemError
	require.NoError(json.Unmarshal(encoded, &decoded))
	assert.Equal(CodeUnsupportedCapability, decoded.Code)
	assert.Equal("merge_mutation", decoded.Details["capability"])
	assert.Equal("gitlab", decoded.Details["provider"])
	assert.Equal("gitlab.example.com", decoded.Details["platformHost"])
}

func TestCodeForStatus(t *testing.T) {
	cases := []struct {
		status int
		want   ProblemCode
	}{
		{http.StatusBadRequest, CodeBadRequest},
		{http.StatusUnauthorized, CodeUnauthorized},
		{http.StatusForbidden, CodeForbidden},
		{http.StatusNotFound, CodeNotFound},
		{http.StatusConflict, CodeConflict},
		{http.StatusRequestEntityTooLarge, CodePayloadTooLarge},
		{http.StatusUnprocessableEntity, CodeValidationError},
		{http.StatusTooManyRequests, CodeRateLimited},
		{http.StatusServiceUnavailable, CodeServiceUnavailable},
		{http.StatusBadGateway, CodeUpstreamError},
		{http.StatusInternalServerError, CodeInternalError},
		{http.StatusNotImplemented, CodeInternalError},
		{http.StatusTeapot, CodeBadRequest},
	}

	assert := Assert.New(t)
	for _, tc := range cases {
		assert.Equal(tc.want, codeForStatus(tc.status), "status %d", tc.status)
	}
}

func TestProblemHelpersSetStatusCodeAndDetails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	cases := []struct {
		name        string
		make        func() huma.StatusError
		wantStatus  int
		wantCode    ProblemCode
		wantDetails map[string]any
	}{
		{
			name:       "BadRequestDefault",
			make:       func() huma.StatusError { return problemBadRequest("", "go away", nil) },
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeBadRequest,
		},
		{
			name: "BadRequestCustomCode",
			make: func() huma.StatusError {
				return problemBadRequest(CodeRepoNotFound, "x", map[string]any{"owner": "acme"})
			},
			wantStatus:  http.StatusBadRequest,
			wantCode:    CodeRepoNotFound,
			wantDetails: map[string]any{"owner": "acme"},
		},
		{
			name:        "Validation",
			make:        func() huma.StatusError { return problemValidation("body.status", "bad", "a", "b") },
			wantStatus:  http.StatusBadRequest,
			wantCode:    CodeValidationError,
			wantDetails: map[string]any{"field": "body.status", "allowed": []string{"a", "b"}},
		},
		{
			name:       "NotFoundDefault",
			make:       func() huma.StatusError { return problemNotFound("", "nope", nil) },
			wantStatus: http.StatusNotFound,
			wantCode:   CodeNotFound,
		},
		{
			name:       "NotFoundCustom",
			make:       func() huma.StatusError { return problemNotFound(CodePullNotFound, "nope", nil) },
			wantStatus: http.StatusNotFound,
			wantCode:   CodePullNotFound,
		},
		{
			name:       "ConflictDefault",
			make:       func() huma.StatusError { return problemConflict("", "boom", nil) },
			wantStatus: http.StatusConflict,
			wantCode:   CodeConflict,
		},
		{
			name:       "Forbidden",
			make:       func() huma.StatusError { return problemForbidden("nope", nil) },
			wantStatus: http.StatusForbidden,
			wantCode:   CodeForbidden,
		},
		{
			name:       "Internal",
			make:       func() huma.StatusError { return problemInternal("boom") },
			wantStatus: http.StatusInternalServerError,
			wantCode:   CodeInternalError,
		},
		{
			name:        "UpstreamWithProviderHost",
			make:        func() huma.StatusError { return problemUpstream("boom", "gitlab", "gitlab.com") },
			wantStatus:  http.StatusBadGateway,
			wantCode:    CodeUpstreamError,
			wantDetails: map[string]any{"provider": "gitlab", "platformHost": "gitlab.com"},
		},
		{
			name:       "UpstreamWithoutContext",
			make:       func() huma.StatusError { return problemUpstream("boom", "", "") },
			wantStatus: http.StatusBadGateway,
			wantCode:   CodeUpstreamError,
		},
		{
			name:        "PayloadTooLarge",
			make:        func() huma.StatusError { return problemPayloadTooLarge("too big", 1024) },
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    CodePayloadTooLarge,
			wantDetails: map[string]any{"maxBytes": int64(1024)},
		},
		{
			name:       "ServiceUnavailable",
			make:       func() huma.StatusError { return problemServiceUnavailable("down") },
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   CodeServiceUnavailable,
		},
		{
			name:        "BranchConflict",
			make:        func() huma.StatusError { return problemBranchConflict("main", "main-fix") },
			wantStatus:  http.StatusConflict,
			wantCode:    CodeBranchConflict,
			wantDetails: map[string]any{"branch": "main", "suggestedBranch": "main-fix"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.make()
			require.NotNil(err)
			pe, ok := err.(*ProblemError)
			require.True(ok, "want *ProblemError, got %T", err)

			assert.Equal(tc.wantStatus, pe.Status)
			assert.Equal(tc.wantStatus, pe.GetStatus())
			assert.Equal(tc.wantCode, pe.Code)
			if tc.wantDetails != nil {
				assert.Equal(tc.wantDetails, pe.Details)
			}
			// Helpers must always populate Title from status.
			assert.NotEmpty(pe.Title)
		})
	}
}

func TestProblemRateLimitedFormatsResetAt(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	reset := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	err := problemRateLimited("github", "github.com", &reset)
	pe, ok := err.(*ProblemError)
	require.True(ok)

	assert.Equal(http.StatusTooManyRequests, pe.Status)
	assert.Equal(CodeRateLimited, pe.Code)
	assert.Equal("github", pe.Details["provider"])
	assert.Equal("github.com", pe.Details["platformHost"])
	assert.Equal("2026-05-19T12:00:00Z", pe.Details["retryAfter"])

	// nil reset → no retryAfter key.
	err = problemRateLimited("github", "github.com", nil)
	pe = err.(*ProblemError)
	_, has := pe.Details["retryAfter"]
	assert.False(has, "details should not contain retryAfter when reset is nil")
}

func TestMapPlatformError(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	reset := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		input       error
		wantNil     bool
		wantStatus  int
		wantCode    ProblemCode
		wantDetails map[string]any
	}{
		{name: "Nil", input: nil, wantNil: true},
		{name: "ContextCanceled", input: context.Canceled, wantNil: true},
		{name: "ContextDeadlineExceeded", input: context.DeadlineExceeded, wantNil: true},
		{
			name: "UnsupportedCapability",
			input: &platform.Error{
				Code:         platform.ErrCodeUnsupportedCapability,
				Provider:     "gitlab",
				PlatformHost: "gitlab.com",
				Capability:   "merge_mutation",
			},
			wantStatus: http.StatusConflict,
			wantCode:   CodeUnsupportedCapability,
			wantDetails: map[string]any{
				"capability":   "merge_mutation",
				"provider":     "gitlab",
				"platformHost": "gitlab.com",
			},
		},
		{
			name: "RateLimited",
			input: &platform.Error{
				Code:         platform.ErrCodeRateLimited,
				Provider:     "github",
				PlatformHost: "github.com",
				ResetAt:      &reset,
			},
			wantStatus: http.StatusTooManyRequests,
			wantCode:   CodeRateLimited,
			wantDetails: map[string]any{
				"provider":     "github",
				"platformHost": "github.com",
				"retryAfter":   "2026-05-19T12:00:00Z",
			},
		},
		{
			name: "PermissionDenied",
			input: &platform.Error{
				Code:         platform.ErrCodePermissionDenied,
				Provider:     "github",
				PlatformHost: "github.com",
				Err:          errors.New("token lacks scope"),
			},
			wantStatus: http.StatusForbidden,
			wantCode:   CodeForbidden,
			wantDetails: map[string]any{
				"provider":     "github",
				"platformHost": "github.com",
			},
		},
		{
			name:       "NotFound",
			input:      &platform.Error{Code: platform.ErrCodeNotFound},
			wantStatus: http.StatusNotFound,
			wantCode:   CodeNotFound,
		},
		{
			name:       "ProviderNotConfigured",
			input:      &platform.Error{Code: platform.ErrCodeProviderNotConfigured},
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeBadRequest,
		},
		{
			name:       "MissingToken",
			input:      &platform.Error{Code: platform.ErrCodeMissingToken},
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeBadRequest,
		},
		{
			name:       "InvalidRepoRef",
			input:      &platform.Error{Code: platform.ErrCodeInvalidRepoRef},
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeBadRequest,
		},
		{
			name:       "PlainError",
			input:      errors.New("boom"),
			wantStatus: http.StatusBadGateway,
			wantCode:   CodeUpstreamError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapPlatformError(tc.input)
			if tc.wantNil {
				assert.Nil(got)
				return
			}
			require.NotNil(got, "expected mapPlatformError to return a problem for %v", tc.input)
			pe, ok := got.(*ProblemError)
			require.True(ok, "want *ProblemError, got %T", got)
			assert.Equal(tc.wantStatus, pe.Status)
			assert.Equal(tc.wantCode, pe.Code)
			if tc.wantDetails != nil {
				assert.Equal(tc.wantDetails, pe.Details)
			}
		})
	}
}

func TestProviderCallProblemDoesNotReturnNilForContextWrappedPlatformError(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	got := providerCallProblem(
		&platform.Error{
			Code: platform.ErrCodeRateLimited,
			Err:  context.Canceled,
		},
		"github",
		"github.com",
	)
	require.NotNil(got)
	pe, ok := got.(*ProblemError)
	require.True(ok, "want *ProblemError, got %T", got)
	assert.Equal(http.StatusBadGateway, pe.Status)
	assert.Equal(CodeUpstreamError, pe.Code)
	require.NotNil(pe.Details)
	assert.Equal("github", pe.Details["provider"])
	assert.Equal("github.com", pe.Details["platformHost"])
}

// TestHumaNewErrorIsReplaced confirms that legacy huma.Error4xx callers
// still flow through to a ProblemError envelope so the migration is
// incremental. As call sites move to typed helpers this test guards
// against regressing the fallback behavior.
func TestHumaNewErrorIsReplaced(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	got := huma.Error400BadRequest("oops")
	pe, ok := got.(*ProblemError)
	require.True(ok, "huma.Error400BadRequest should now return *ProblemError, got %T", got)
	assert.Equal(http.StatusBadRequest, pe.Status)
	assert.Equal(CodeBadRequest, pe.Code)
	assert.Equal("oops", pe.Detail)

	// huma.NewError with non-nil errs[i] populates Errors[] for parity.
	got = huma.Error400BadRequest("oops", fmt.Errorf("inner"))
	pe = got.(*ProblemError)
	require.Len(pe.Errors, 1)
	assert.Equal("inner", pe.Errors[0].Message)

	// A 502 returns the upstreamError default code.
	got = huma.Error502BadGateway("boom")
	pe = got.(*ProblemError)
	assert.Equal(CodeUpstreamError, pe.Code)
}
