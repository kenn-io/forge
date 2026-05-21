# Typed RFC 9457 Error Envelope — Design Spec

**Status:** Drafted 2026-05-19 (worktree: agent-a8b603b4d8d1dd2ee).

## Problem

Every failure path in `internal/server/` returns an `application/problem+json`
body whose only machine-readable signal is the HTTP status code. The
human-readable `detail` is the only field the Svelte UI can branch on.
The UI substring-matches that prose to decide which recovery affordance to
show (re-auth, retry countdown, capability missing). This couples UI
behavior to backend prose, breaks when the prose is reworded, and prevents
typed handling on the frontend.

`internal/server/capabilities.go` already has a partial typed
implementation: it returns `huma.NewError(...)` with a side-channel
`ErrorDetail.Value` map carrying `{code, provider, platform_host,
capability}`. The `code` value lives inside `errors[0].value.code` rather
than on the envelope itself, and only the capability-gate path uses it.

## Goal

Every failure path in `internal/server/` returns an RFC 9457 error envelope
with a top-level, machine-readable `code` (camelCase) and a top-level
`details` object. Two codes are required by spec:

- `unsupportedCapability`, with `details.capability` carrying the missing
  capability name.
- `rateLimited`, with `details.retryAfter` carrying the reset time
  (RFC 3339 string).

Plus enough additional codes to cover every existing handler call site
without losing semantic precision.

A grep for `huma.Error4` or `huma.Error5` in `internal/server/` returns
nothing outside test fixtures and the new helper module itself.

The generated TypeScript client surfaces `code` as a typed string union
(via OpenAPI `enum`). A Svelte error boundary branches on `code` as a
worked example.

Wire-level integration tests assert on `code` and `details` for at least
three failure paths: `unsupportedCapability`, `rateLimited`, and one
additional path (chosen: input validation, code `validationError`).

## Non-goals

- Migrating tests or generated OpenAPI docs to a new format.
- Adding new HTTP statuses or new error semantics. Same statuses, same
  triggers, richer body.
- Translating any backend prose to a different locale. `detail` and
  `title` stay English.
- Restructuring `internal/platform/errors.go`. Its `PlatformErrorCode`
  snake_case constants stay as-is; they're mapped to camelCase wire codes
  at the `internal/server/` boundary.
- Wiring every Svelte component to branch on `code`. One worked example
  on the capability gate is the deliverable; broader rollout is a
  follow-up.

## Decisions

### D1: Code naming convention is camelCase on the wire

The task spec hard-codes `unsupportedCapability` and `rateLimited` as
wire literals. Every other code uses the same convention. Internal Go
`PlatformErrorCode` (`unsupported_capability`, `rate_limited`, etc.) stay
snake_case; the boundary at `internal/server/` translates them.

### D2: Replace `huma.NewError` with a custom builder

Huma exposes `var NewError` as the central error constructor. Every
`huma.Error4xx`/`Error5xx` helper calls through it. We replace the global
with a builder that returns our own `ProblemError` type implementing
`huma.StatusError` and `huma.ContentTypeFilter`. Existing call sites still
work; new wire fields appear automatically.

`ProblemError` shape:

```go
type ProblemError struct {
    // RFC 9457 core
    Type     string `json:"type,omitempty" format:"uri" default:"about:blank"`
    Title    string `json:"title,omitempty"`
    Status   int    `json:"status,omitempty"`
    Detail   string `json:"detail,omitempty"`
    Instance string `json:"instance,omitempty"`

    // huma compat (kept for parity; populated when callers pass details)
    Errors []*huma.ErrorDetail `json:"errors,omitempty"`

    // RFC 9457 extension members
    Code    ErrorCode      `json:"code" enum:"badRequest,notFound,..." doc:"Machine-readable error code"`
    Details map[string]any `json:"details,omitempty" doc:"Machine-readable error context"`
}
```

`enum` is generated from the master code list.

### D3: A new helper module `internal/server/problems.go`

All construction goes through `problemError(status, code, detail,
details...)` and convenience wrappers `problemBadRequest`,
`problemNotFound`, `problemConflict`, `problemUpstream`,
`problemInternal`, `problemUnsupportedCapability`, `problemRateLimited`,
etc. Wrappers fill in the right status + code automatically.

`huma.Error4xx`/`Error5xx` callers are migrated en masse to the
appropriate wrapper. After migration, a grep for the huma helpers under
`internal/server/` is empty.

### D4: Code taxonomy (initial)

| Wire code               | Status | When                                                     | `details` shape                                           |
|-------------------------|-------:|----------------------------------------------------------|-----------------------------------------------------------|
| `badRequest`            |    400 | Generic 400 fallback                                     | none                                                      |
| `validationError`       |    400 | Input validation (allowed-values, blank, must-be-format) | `{field?: string, allowed?: []string}`                    |
| `notFound`              |    404 | Generic 404 fallback                                     | none                                                      |
| `repoNotFound`          |    404 | Repo by (provider, host, owner, name) lookup miss        | `{provider, host, owner, name}` (when known)              |
| `pullNotFound`          |    404 | Pull by (repo, number) miss                              | `{number?: int}`                                          |
| `issueNotFound`         |    404 | Issue by (repo, number) miss                             | `{number?: int}`                                          |
| `commentNotFound`       |    404 | Comment by (mr/issue, id) miss                           | `{commentId?: int}`                                       |
| `workspaceNotFound`     |    404 | Workspace lookup miss                                    | none                                                      |
| `projectNotFound`       |    404 | Local-project record miss                                | none                                                      |
| `settingsUnavailable`   |    404 | Settings store nil (read-only middleman)                 | none                                                      |
| `forbidden`             |    403 | Generic 403 fallback                                     | none                                                      |
| `unsupportedCapability` |    409 | Provider lacks a capability the request needs            | `{capability, provider, platformHost}`                    |
| `conflict`              |    409 | Generic 409 fallback                                     | none                                                      |
| `branchConflict`        |    409 | Existing local branch blocks workspace creation          | `{branch, suggestedBranch}`                               |
| `payloadTooLarge`       |    413 | Body exceeds limit                                       | `{maxBytes?: int}`                                        |
| `rateLimited`           |    429 | Upstream provider 429                                    | `{retryAfter?: string, provider?, platformHost?}`         |
| `internalError`         |    500 | Generic 5xx fallback                                     | none                                                      |
| `upstreamError`         |    502 | Provider API failure (auth, 5xx, network)                | `{provider?, platformHost?}`                              |
| `serviceUnavailable`    |    503 | Stack health 503                                         | none                                                      |

Codes are added to the master enum in `internal/server/problems.go`. The
enum order is alphabetical for stable OpenAPI diffs.

### D5: Translation from platform layer

`internal/server/` wraps each platform call with `mapPlatformError(err)`
that switches on `errors.As(err, &platform.Error{})` and returns the
appropriate `ProblemError`:

- `ErrCodeUnsupportedCapability` → `problemUnsupportedCapability(...)`
- `ErrCodeRateLimited` → `problemRateLimited(...)` (uses platform's
  `ResetAt` for `details.retryAfter`)
- `ErrCodePermissionDenied` → `problemForbidden(...)` with platform context
- `ErrCodeNotFound` → `problemNotFound(...)` (caller may override with a
  more specific code like `repoNotFound`)
- `ErrCodeProviderNotConfigured`, `ErrCodeMissingToken`,
  `ErrCodeInvalidRepoRef` → `problemBadRequest(...)` (these are
  config-time issues exposed at request time)

### D6: OpenAPI surfaces the enum

The `enum:"..."` tag on `ProblemError.Code` causes huma to emit an OpenAPI
schema enum. `openapi-typescript` turns that into a TS string-literal
union: `code: "badRequest" | "notFound" | "unsupportedCapability" | ...`.
The frontend imports `components["schemas"]["ErrorModel"]` (same name —
huma uses the type name; we name the Go type `ErrorModel` for backward
compatibility, with `ProblemError` as an alias).

Actually: keeping the Go type name `ErrorModel` would shadow huma's
own. Instead we name the type `ProblemError` and rename the OpenAPI
schema via huma's `huma.SchemaRef` machinery to keep the on-wire schema
name stable. If that proves harder than expected, allow the OpenAPI
schema name to become `ProblemError` and update generated TS imports —
the change is tracked by `make api-generate`.

### D7: Frontend worked example

`packages/ui/src/api/problems.ts` exports:

```ts
export const ProblemCode = {
  unsupportedCapability: "unsupportedCapability",
  rateLimited: "rateLimited",
  ...
} as const;
export type ProblemCode = (typeof ProblemCode)[keyof typeof ProblemCode];
```

Plus an `isProblem(value): value is ProblemBody` type guard.

The capability-disabled UI surface (today a grey-out + tooltip) becomes a
branch on `ProblemCode.unsupportedCapability` in the existing component
that consumes `provider-capabilities.ts`. A test in
`packages/ui/src/api/problem-handlers.test.ts` mocks a `fetch` response
with a problem+json body and asserts the branch fires.

### D8: Tests

Wire-level integration tests in `internal/server/api_test.go`:

1. `TestAPIUnsupportedCapabilityEnvelope`: Trigger a capability-gated
   route against the gitlab fake; assert top-level `code ==
   "unsupportedCapability"` and `details.capability == "<expected>"`.
2. `TestAPIRateLimitedEnvelope`: Inject a `platform.Error` with
   `ErrCodeRateLimited` through a mock provider; assert `code ==
   "rateLimited"` and `details.retryAfter` is a parseable RFC 3339 string.
3. `TestAPIValidationErrorEnvelope`: Send an invalid `setKanbanState`
   body; assert `code == "validationError"` and `details.field ==
   "status"`, `details.allowed == ["new","reviewing",...]`.

The existing `assertUnsupportedCapabilityProblem` helper is rewritten to
check the top-level `code` and `details` instead of `errors[0].value`.

## Migration plan

1. Land `internal/server/problems.go` + replace `huma.NewError`. Tests
   still pass because the legacy `Error4xx("msg")` paths flow through
   the new builder with `code = <statusToCode>`.
2. Rewrite the seven offender files (`huma_routes.go`,
   `settings_handlers.go`, `repo_import_handlers.go`,
   `projects_handlers.go`, `label_handlers.go`, `health_routes.go`,
   `capabilities.go`) call site by call site, picking the best code from
   the taxonomy. Each migrated site loses the `huma.Error4xx`/`Error5xx`
   token. Run `make test` after each file.
3. Update `assertUnsupportedCapabilityProblem` and tests it underpins.
4. Regenerate API artifacts: `make api-generate`. Verify the TS schema
   contains the enum.
5. Add the TS `ProblemCode` helper + a Svelte branch on
   `unsupportedCapability` in the existing capability gate UI.
6. Add the three wire-level integration tests.
7. `make lint`, `make vet`, `make test-short`, full frontend test as
   needed.

## Risks

- Replacing `huma.NewError` is global to the binary, not scoped per huma
  instance. Acceptable because we own the only huma instance (the server),
  and the helpers exist in the same package.
- A few error sites today carry `errors[].value` payloads (e.g.
  branch-conflict). Those move into top-level `details`. The legacy
  `errors[]` field stays populated for huma-internal validation messages
  so we don't break introspection.
- OpenAPI schema rename (`ErrorModel` → `ProblemError`): callers of the
  generated `ErrorModel` symbol (Go: `generated.ErrorModel`, TS:
  `components["schemas"]["ErrorModel"]`) need updating. Plan: keep the
  Go-side `ErrorModel` name on the wire schema via the registry hint, so
  generated artifacts keep the existing symbol. We add `Code` and
  `Details` fields to the existing wire schema rather than renaming.
- Pre-existing snake_case `code` value at
  `errors[0].value.code = "unsupported_capability"` is removed because
  it's superseded by the top-level camelCase `code`. The legacy ad-hoc
  field is unused outside the test helper.
