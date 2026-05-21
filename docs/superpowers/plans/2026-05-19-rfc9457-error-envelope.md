# RFC 9457 Error Envelope Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every `internal/server/` failure path returns a top-level RFC 9457 envelope with `code` (camelCase) and `details`, and the frontend has a typed worked example.

**Architecture:** Replace `huma.NewError` with a custom `ProblemError` builder that adds `code` + `details` to the existing wire schema. Add a `problems.go` helper module owning the code taxonomy. Migrate every `huma.Error4xx`/`Error5xx` call site through the new helpers. Surface the enum to TS via OpenAPI `enum` tag.

**Tech Stack:** Go 1.24, huma v2, openapi-typescript, Svelte 5, testify.

---

### Task 1: Define the code taxonomy and ProblemError type

**Files:**
- Create: `internal/server/problems.go`
- Create: `internal/server/problems_test.go`

- [ ] **Step 1: Author `ProblemError` type**

In `internal/server/problems.go`, define `ProblemError` mirroring `huma.ErrorModel`'s wire fields and adding `Code` (string, enum-tagged) and `Details` (`map[string]any`). Implement `Error() string`, `GetStatus() int`, `ContentType(string) string` returning `application/problem+json` for `application/json`. Mark with json tags that match RFC 9457.

The struct must register as schema `ErrorModel` so the generated client symbols stay stable. Use a `_ struct{ Name string }` field with the huma `name:` tag pattern, or override via `huma.SchemaRef`. Verify by inspecting the generated YAML after `make api-generate`.

- [ ] **Step 2: Master code constants**

Define a `ProblemCode` string type and a const block for every wire code in the taxonomy (`badRequest`, `notFound`, `unsupportedCapability`, `rateLimited`, …, alphabetical for stable OpenAPI). Add `allProblemCodes()` returning the slice for the `enum` tag value at init.

The `Code` field uses `enum:"badRequest,...,..."` built from `allProblemCodes()` joined with commas. To do that at compile time, hard-code the enum tag with the same alphabetical order; add a unit test that asserts the constant slice matches the tag.

- [ ] **Step 3: Helper constructors**

Add helpers:
- `problemError(status int, code ProblemCode, detail string, details map[string]any) huma.StatusError`
- `problemBadRequest(code ProblemCode, detail string, details ...detailKV) huma.StatusError`
- `problemNotFound(code ProblemCode, detail string, details ...detailKV) huma.StatusError`
- `problemConflict(code ProblemCode, detail string, details ...detailKV) huma.StatusError`
- `problemForbidden(detail string, details ...detailKV) huma.StatusError`
- `problemInternal(detail string) huma.StatusError`
- `problemUpstream(detail string, details ...detailKV) huma.StatusError`
- `problemUnsupportedCapability(repo db.Repo, capability string) huma.StatusError`
- `problemRateLimited(provider, host string, resetAt *time.Time) huma.StatusError`
- `problemValidation(field string, detail string, allowed ...string) huma.StatusError`
- `problemBranchConflict(branch, suggested string) huma.StatusError`
- `problemPayloadTooLarge(maxBytes int64) huma.StatusError`
- `problemServiceUnavailable(detail string) huma.StatusError`

`detailKV` is a small helper struct used as variadic to make call sites readable: `detailKV{Key: "field", Value: "status"}`. Or pass `map[string]any{...}` directly — pick whichever is less verbose at the call sites; document the choice in the helper file.

- [ ] **Step 4: Replace `huma.NewError`**

In `init()` of `internal/server/problems.go`, set `huma.NewError = ...` to a function that returns a `*ProblemError` with `Code = codeForStatus(status)`. `codeForStatus` maps 400→badRequest, 401→unauthorized, 403→forbidden, 404→notFound, 409→conflict, 413→payloadTooLarge, 422→validationError, 429→rateLimited, 500→internalError, 502→upstreamError, 503→serviceUnavailable, default→internalError.

Existing `huma.Error4xx("msg")` callers continue to compile and produce an envelope; they just get a code derived from status. Migration in later tasks overrides the code where richer semantics matter.

- [ ] **Step 5: Unit tests for problems.go**

Add table-driven tests in `internal/server/problems_test.go`:
- Each helper sets the right status, code, and details keys.
- `codeForStatus` returns the right code for every status in the taxonomy.
- The enum tag string includes every constant declared.
- `ContentType("application/json")` returns `application/problem+json`.
- A round-trip JSON encode/decode of a `ProblemError` preserves `code` and `details`.

```bash
go test ./internal/server -run TestProblems -shuffle=on
git commit -m "feat(server): add typed ProblemError envelope and helpers"
```

---

### Task 2: Map platform-layer errors to wire codes

**Files:**
- Modify: `internal/server/problems.go` (add `mapPlatformError`)
- Modify: `internal/server/problems_test.go`

- [ ] **Step 1: `mapPlatformError(err error) huma.StatusError`**

Use `errors.As(err, &platformErr *platform.Error)` to read `Code`, `Provider`, `PlatformHost`, `Capability`, `ResetAt`. Return:
- `ErrCodeUnsupportedCapability` → `problemUnsupportedCapability` with provider context
- `ErrCodeRateLimited` → `problemRateLimited` (`details.retryAfter` from `ResetAt.UTC().Format(time.RFC3339)` if non-nil)
- `ErrCodePermissionDenied` → `problemForbidden`
- `ErrCodeNotFound` → `problemNotFound("notFound", ...)`
- `ErrCodeProviderNotConfigured`, `ErrCodeMissingToken`, `ErrCodeInvalidRepoRef` → `problemBadRequest`
- default → `problemUpstream(err.Error(), ...)`

Return `nil` for `nil` input and for `context.Canceled`/`DeadlineExceeded`.

- [ ] **Step 2: Tests**

Table-driven test in `problems_test.go`: each platform code maps to the expected wire code and status; `nil` input returns `nil`; context errors pass through.

```bash
go test ./internal/server -run TestMapPlatformError -shuffle=on
git commit -m "feat(server): map platform.Error codes to wire problems"
```

---

### Task 3: Migrate `internal/server/capabilities.go`

**Files:**
- Modify: `internal/server/capabilities.go`

- [ ] **Step 1: Replace `unsupportedCapabilityProblem` body**

Use `problemUnsupportedCapability(repo, capability)` directly. Delete the `unsupportedCapabilityDetail` struct and its `ErrorDetail()` method — the new helper carries everything in top-level `details`.

- [ ] **Step 2: Verify `requireRepoRouteCapability` still wraps lookup errors**

`providerRouteLookupError(err)` is the other escape. Leave it pointing at the new helpers (we'll migrate it in Task 4).

```bash
go test ./internal/server -shuffle=on -run TestAPIGitlab|TestAPI.*Capability
git commit -m "refactor(server): migrate capability gate to typed problem"
```

---

### Task 4: Migrate `internal/server/huma_routes.go`

**Files:**
- Modify: `internal/server/huma_routes.go`

This is the bulk of the work (245 call sites). The approach:

- [ ] **Step 1: Rewrite `providerRouteLookupError`**

`errRepoPathRequired` → `problemBadRequest("badRequest", err.Error())`.
`errRepoNotFound` → `problemNotFound("repoNotFound", "repo not found", detailKV{Key: "owner", Value: ...}, ...)`. The function has the parsed owner/name in scope via the call site; refactor signature if needed to thread that context. Simpler alternative: keep the function thin and have callers compose richer `details` outside it.

`platform_host is required`/`unsupported platform` → `problemBadRequest("badRequest", err.Error())`.

Default → `problemInternal("get repo failed")`.

- [ ] **Step 2: Sweep huma_routes.go for `huma.Error400BadRequest`**

For each occurrence:
- Input-validation case (string-set check, blank check, must-be-non-empty) → `problemValidation(field, detail, allowed...)` with the right `field` and `allowed` list.
- Bad request that wraps an `err.Error()` → `problemBadRequest("badRequest", err.Error())`.
- Anything else → `problemBadRequest("badRequest", detail)`.

The `validationError` cases include: status enum, kanban state enum, merge method enum, commit-range constraints, comment-body empty, title-blank.

- [ ] **Step 3: Sweep for `huma.Error404NotFound`**

- "repo not found" → `problemNotFound("repoNotFound", ...)`.
- "pull request not found" / "pull request" lookup miss → `problemNotFound("pullNotFound", ...)`.
- "merge request not found" → `problemNotFound("pullNotFound", ...)` (it's the same concept on the wire).
- "issue not found" → `problemNotFound("issueNotFound", ...)`.
- "comment not found" / "comment not found for pull request" → `problemNotFound("commentNotFound", ...)`.
- "project not found" → `problemNotFound("projectNotFound", ...)`.
- "workspace summary missing" → `problemNotFound("workspaceNotFound", ...)`.
- "settings not available" → `problemNotFound("settingsUnavailable", ...)`.
- Generic fallthrough → `problemNotFound("notFound", err.Error())`.

- [ ] **Step 4: Sweep for `huma.Error409Conflict`**

- `IssueWorkspaceBranchConflictError` path → `problemBranchConflict(branch, suggested)`. The existing inline `&huma.ErrorModel{Type:..., Title:..., Errors:...}` constructor is rewritten to the helper; the `Type` URI carrier (`issueWorkspaceBranchConflictType`) becomes `details.type` if still needed for callers.
- All other 409 → `problemConflict("conflict", detail, ...)`.

- [ ] **Step 5: Sweep for `huma.Error500InternalServerError`, `huma.Error502BadGateway`, `huma.Error503ServiceUnavailable`, `huma.Error413RequestEntityTooLarge`, `huma.Error403Forbidden`**

- 500 → `problemInternal(detail)`.
- 502 with provider error message → `problemUpstream(detail, ...details...)`. Pass through provider+host when known.
- 503 → `problemServiceUnavailable(detail)`.
- 413 → `problemPayloadTooLarge(maxBytes)`.
- 403 → `problemForbidden(detail)`.

- [ ] **Step 6: Verify the grep**

```bash
rg -n "huma\.Error[0-9]" internal/server/huma_routes.go
```

Returns nothing.

- [ ] **Step 7: Run server tests**

```bash
go test ./internal/server -shuffle=on -short
git commit -m "refactor(server): migrate huma_routes.go to typed problem helpers"
```

---

### Task 5: Migrate remaining handler files

**Files:**
- Modify: `internal/server/settings_handlers.go`
- Modify: `internal/server/repo_import_handlers.go`
- Modify: `internal/server/projects_handlers.go`
- Modify: `internal/server/label_handlers.go`
- Modify: `internal/server/health_routes.go`

- [ ] **Step 1: Apply the same patterns from Task 4 to each file**

The same heuristic: validation → `problemValidation`, lookup miss → `problemNotFound(<specific>)`, provider failure → `problemUpstream`, 500 → `problemInternal`.

Specific gotchas:
- `label_handlers.go` has duplicate-label, missing-from-catalog, must-be-array errors. Each gets `problemValidation` with a meaningful `field` and (for missing-from-catalog) `details.label`.
- `repo_import_handlers.go` has "GitHub API error" and "Provider API error" branches. Both become `problemUpstream`; pass provider+host in `details` when in scope.
- `projects_handlers.go` has filesystem checks ("local_path does not exist", "is not a directory"). These get `problemValidation` with `field: "local_path"`.
- `health_routes.go` 503s become `problemServiceUnavailable` with whatever shape the existing detail carried.

- [ ] **Step 2: Verify the grep**

```bash
rg -n "huma\.Error[0-9]" internal/server/
```

Returns at most matches inside `problems.go` and inside test files (no production handler should have any).

- [ ] **Step 3: Run server tests**

```bash
go test ./internal/server -shuffle=on -short
git commit -m "refactor(server): migrate remaining handlers to typed problems"
```

---

### Task 6: Translate platform errors at provider call sites

**Files:**
- Modify: `internal/server/huma_routes.go` (and any other handler that catches `*platform.Error`)

- [ ] **Step 1: Find provider call sites**

```bash
rg -n "platform\.Error|platform\.Err[A-Z]" internal/server/
```

For every handler that today returns a generic 502 after a provider failure but receives a `*platform.Error`, replace the literal handler-side string match with `mapPlatformError(err)`. Handlers that intercept a specific platform code first (e.g. for a custom message) still can; they just call the new helpers instead of `huma.Error...`.

- [ ] **Step 2: Wire rate-limit response**

In `internal/server/huma_routes.go`, the rate-limit tracker exposes `rt.ResetAt()`. When a provider call returns a 429-flavored error and we have the tracker in scope, `problemRateLimited(provider, host, rt.ResetAt())` populates `details.retryAfter`. When the tracker isn't in scope, fall through to `mapPlatformError` which uses `platform.Error.ResetAt`.

- [ ] **Step 3: Tests**

Will be added in Task 9; no test gate at this commit.

```bash
go test ./internal/server -shuffle=on -short
git commit -m "feat(server): translate platform error codes to wire problems"
```

---

### Task 7: Update the existing capability-test helper and any tests asserting on `errors[].value.code`

**Files:**
- Modify: `internal/server/api_test.go` (`assertUnsupportedCapabilityProblem`)
- Modify: any test that asserts on `errors[0].value.code` or `errors[0].message == "unsupported_capability"`

- [ ] **Step 1: Rewrite `assertUnsupportedCapabilityProblem`**

Decode into a struct exposing `Code string`, `Details map[string]any`. Assert:
- `Code == "unsupportedCapability"`
- `Details["capability"] == capability`
- `Details["provider"] == provider`
- `Details["platformHost"] == host`
- Legacy `errors[]` either gone or no longer asserted on.

- [ ] **Step 2: rg for any other test asserting on errors[].value**

```bash
rg -n "Errors\[0\]\.Value|errors\[0\]\.value" internal/server/ frontend/ packages/
```

Migrate each to the top-level fields. The `branch_conflict` path (`IssueWorkspaceBranchConflictError`) currently asserts on Errors[]; update to assert `details.branch` and `details.suggestedBranch`.

- [ ] **Step 3: Run server tests**

```bash
go test ./internal/server -shuffle=on -short
git commit -m "test(server): assert on typed problem fields instead of errors[].value"
```

---

### Task 8: Regenerate API artifacts and TS schema

**Files:**
- Modify: `frontend/openapi/openapi.yaml` (generated)
- Modify: `internal/apiclient/spec/openapi.json` (generated)
- Modify: `internal/apiclient/generated/client.gen.go` (generated)
- Modify: `packages/ui/src/api/generated/schema.ts` (generated)
- Modify: `packages/ui/src/api/generated/client.ts` (generated)

- [ ] **Step 1: Run `make api-generate`**

```bash
make api-generate
```

- [ ] **Step 2: Verify outputs**

- `frontend/openapi/openapi.yaml`: search for `code:` and confirm the enum is present with the full taxonomy.
- `packages/ui/src/api/generated/schema.ts`: confirm `ErrorModel.code` is a string-literal union covering every code.
- `internal/apiclient/generated/client.gen.go`: confirm `ErrorModel.Code` is a `*string` (or whatever shape oapi-codegen picks) on the Go side.

If the wire schema name changed from `ErrorModel`, restore via the registry hint in `problems.go` (Task 1 Step 1).

- [ ] **Step 3: Commit generated artifacts**

```bash
make lint vet
git commit -m "chore: regenerate API artifacts with problem code enum"
```

---

### Task 9: Wire-level integration tests

**Files:**
- Modify: `internal/server/api_test.go`

- [ ] **Step 1: `TestAPIUnsupportedCapabilityEnvelope`**

Reuse `setupGitlabCapabilityTestServer`. Trigger any capability-gated mutation route (e.g. workflow approval) and decode the body into a struct with `Code`, `Details`. Assert `Code == "unsupportedCapability"` and `Details["capability"] == "workflow_approval"`.

- [ ] **Step 2: `TestAPIRateLimitedEnvelope`**

Use a mock provider client that returns `platform.ErrRateLimited` with a non-nil `ResetAt`. Make a request that hits that provider. Assert `Code == "rateLimited"` and `Details["retryAfter"]` parses as RFC 3339.

- [ ] **Step 3: `TestAPIValidationErrorEnvelope`**

Send an invalid `setKanbanState` body (e.g. `status: "frobnicated"`). Assert `Code == "validationError"`, `Details["field"] == "status"`, `Details["allowed"]` is the expected slice.

- [ ] **Step 4: Run**

```bash
go test ./internal/server -shuffle=on -run TestAPI.*Envelope
git commit -m "test(server): wire-level RFC 9457 envelope coverage"
```

---

### Task 10: Frontend TypeScript helper and Svelte worked example

**Files:**
- Create: `packages/ui/src/api/problems.ts`
- Create: `packages/ui/src/api/problems.test.ts`
- Modify: one existing Svelte component that already branches on capability (find via `rg "capability" packages/ui/src/components/`).

- [ ] **Step 1: `problems.ts`**

Export:
- `type ProblemBody` mirroring the generated schema fields (or import the schema directly).
- `type ProblemCode` from the generated union: `type ProblemCode = NonNullable<components["schemas"]["ErrorModel"]["code"]>` (or whatever it ends up named).
- A `const ProblemCodes` value object with each code so other modules can branch via `case ProblemCodes.unsupportedCapability:`.
- `isProblem(value: unknown): value is ProblemBody` type guard.
- `async function readProblem(response: Response): Promise<ProblemBody | null>` that returns null if `response.ok` or the body isn't problem+json.

- [ ] **Step 2: Unit tests for `problems.ts`**

`packages/ui/src/api/problems.test.ts`: mock fetch responses for problem+json bodies; assert the type guard and reader behave correctly.

- [ ] **Step 3: Svelte branch on `unsupportedCapability`**

In the existing capability-driven UI (find via `provider-capabilities` consumers), when an API call rejects, read the problem with `readProblem`, and if `code === ProblemCodes.unsupportedCapability`, show the capability-disabled affordance with `body.details.capability` in the tooltip. Otherwise fall back to the current generic failure UI.

The exact component is whatever today already hides/disables based on `provider-capabilities.ts`. If no live error path exists, add the example to the existing comment-submit flow or the merge-button flow — those already call providers.

- [ ] **Step 4: Run frontend tests**

```bash
cd packages/ui && bun run test
git commit -m "feat(ui): branch on RFC 9457 problem code for capability gate"
```

---

### Task 11: Final verification

- [ ] **Step 1: Verify the grep**

```bash
rg -n "huma\.Error[0-9]" internal/server/
```

Only matches inside `problems.go` and the seed file are acceptable.

- [ ] **Step 2: Lint, vet, full test**

```bash
make lint && make vet && make test-short
```

- [ ] **Step 3: Generated-artifact diff sanity**

`git diff main -- frontend/openapi/openapi.yaml | head -200` should show the new `code` enum on `ErrorModel`. `git diff main -- packages/ui/src/api/generated/schema.ts | head -100` should show the same union on the TS schema.

No commit; this task is purely the verification step before stopping.
