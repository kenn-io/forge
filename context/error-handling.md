# Error Handling

Use this document for changes that touch HTTP API failure responses, platform
error translation, generated API clients, or frontend behavior that branches on
server errors. Retry and scheduling policy lives in
[`context/retries-and-backoffs.md`](./retries-and-backoffs.md).

## API Problem Envelope

API handlers should return RFC 9457 `application/problem+json` responses for
failure paths. The envelope keeps the standard fields and adds stable extension
members:

```json
{
  "type": "about:blank",
  "title": "Conflict",
  "status": 409,
  "detail": "provider does not support workflow approval",
  "code": "unsupportedCapability",
  "details": {
    "capability": "workflow_approval",
    "provider": "gitlab",
    "platformHost": "gitlab.com"
  }
}
```

`detail` and `title` are human-readable and may change. UI code, generated
clients, tests, and automation should branch on `code` and `details`, not on
prose.

## Code Taxonomy

Wire codes are camelCase. Keep internal platform error constants in their native
snake_case form and translate them at the `internal/server` boundary.

| Wire code | Status | Use |
| --- | ---: | --- |
| `badRequest` | 400 | Generic malformed request fallback. |
| `validationError` | 400 | Input validation such as blank fields, invalid formats, or allowed-value checks. Include `details.field`; include `details.allowed` when useful. |
| `forbidden` | 403 | Authenticated caller or token lacks permission. |
| `notFound` | 404 | Generic not-found fallback. |
| `repoNotFound` | 404 | Repository lookup miss by provider-aware identity. |
| `pullNotFound` | 404 | Pull or merge request lookup miss. |
| `issueNotFound` | 404 | Issue lookup miss. |
| `commentNotFound` | 404 | Comment lookup miss. |
| `projectNotFound` | 404 | Local project record lookup miss. |
| `workspaceNotFound` | 404 | Workspace lookup miss. |
| `settingsUnavailable` | 404 | Settings store is unavailable in the current server mode. |
| `conflict` | 409 | Generic state conflict. |
| `branchConflict` | 409 | Local workspace branch already exists. Include `details.branch` and `details.suggestedBranch`. |
| `unsupportedCapability` | 409 | Provider lacks the operation capability. Include `details.capability`, `details.provider`, and `details.platformHost`. |
| `payloadTooLarge` | 413 | Request body exceeds the accepted size. Include `details.maxBytes` when known. |
| `rateLimited` | 429 | Upstream provider quota is exhausted. Include `details.retryAfter` as a UTC RFC3339 timestamp when known. |
| `internalError` | 500 | Generic middleman bug or unexpected local failure. |
| `upstreamError` | 502 | Provider API, auth, network, or upstream service failure. Include provider identity when known. |
| `serviceUnavailable` | 503 | Temporarily unavailable local service or health dependency. |

Add new codes only when the frontend or an API client needs a distinct recovery
branch. Keep the OpenAPI enum stable and regenerate API artifacts with
`make api-generate` after changing the taxonomy.

## Server Construction

`internal/server` owns HTTP error construction. Prefer package helpers over
direct `huma.Error4xx` / `huma.Error5xx` calls in production handlers so status,
wire code, and details stay consistent.

Rules for handler code:

- Validation failures use `validationError` and should name the request field.
- Provider capability gates use `unsupportedCapability`; do not hide unsupported
  mutations behind GitHub-only behavior.
- Rate-limit responses use `rateLimited` and carry a retry timestamp when a
  provider tracker or platform error exposes one.
- Not-found errors should use the most specific domain code available instead
  of the generic `notFound` fallback.
- Branch-conflict payloads put branch names in top-level `details`; do not rely
  on nested `errors[].value` payloads.
- Huma's `errors[]` field is reserved for Huma validation compatibility. Do not
  add new machine-readable contracts there.

## Platform Error Translation

Translate `internal/platform` typed errors at the server boundary:

| Platform code | Wire result |
| --- | --- |
| `unsupported_capability` | `409 unsupportedCapability` |
| `rate_limited` | `429 rateLimited` |
| `permission_denied` | `403 forbidden` |
| `not_found` | `404 notFound`, or a more specific not-found code when the caller knows the resource type |
| `provider_not_configured`, `missing_token`, `invalid_repo_ref` | `400 badRequest` |
| Unknown provider/platform failures | `502 upstreamError` |

Context cancellation and deadline errors should pass through cancellation paths
instead of being wrapped as provider failures.

## Frontend Handling

Generated TypeScript schemas should expose the problem `code` enum. Shared UI
helpers should provide:

- an `isProblem(value)` type guard;
- a `readProblem(response)` helper for `application/problem+json` responses;
- typed accessors for common `details` members such as `capability` and
  `retryAfter`.

Components may still display `detail` as user-facing text, but behavior must use
the typed code. Examples: disable or explain unavailable provider operations
from `unsupportedCapability`, and show retry timing from `rateLimited`.

## Tests

Use wire-level server tests with real SQLite for API error contracts. Coverage
should assert status, content type, top-level `code`, and relevant `details`.
At minimum, protect:

- `unsupportedCapability` through a provider capability-gated mutation;
- `rateLimited` through a fake provider/platform error with a reset time;
- `validationError` through a request with an invalid enum or blank required
  field.

Tests should not assert on human-readable prose unless the prose itself is the
feature under test. Run Go tests with `-shuffle=on`; use generated clients for
integration-style API tests when practical.
