# Wire-level HTTP testing discipline — design

## Purpose

middleman has three test-shape conventions for the HTTP layer today, and the
boundary between them is unwritten:

- `internal/server/*_test.go` — handler-internal tests in `package server`,
  using `httptest.NewRecorder` + `srv.ServeHTTP` (via `doJSON` or a
  `roundTripFunc` transport), or — rarely — direct handler-function calls.
- `internal/server/apitest/` — black-box tests in a sibling package, using the
  generated OpenAPI client through the same recorder transport.
- `internal/server/e2etest/` — black-box tests using a real
  `httptest.NewServer` socket, used today mostly for health and SSE flows.

A class of bugs slips through because handler-internal tests inspect the
handler's return value rather than the response a client would actually
receive:

- JSON time field serialization changes (`Z` vs `+00:00`).
- New error codes that don't appear in the OpenAPI document.
- Headers set in the handler but stripped or overridden by middleware later.
- Status codes the handler returns but middleware overrides.

This design documents a forward-looking discipline. It does not migrate the
existing test suite. It adds doctrine plus three worked examples that show the
shape new tests should take.

## Operational definition

A test is **wire-level** when both of the following hold:

1. It exercises the request through `srv.ServeHTTP` — that is, every middleware
   the production server installs runs against the test request.
2. It asserts on the response a client would actually observe: status code,
   response headers, and response body bytes. The handler function's return
   value is not consulted.

Two transports satisfy this definition:

- **In-process via `httptest.NewRecorder`** is the default for request /
  response tests. Used by `internal/server/apitest/` and the existing in-package
  `doJSON` helper. Fast, no port allocation, deterministic. Fires every
  middleware. Does not faithfully simulate streaming I/O: there is no
  `net.Conn` behind the recorder, the recorder buffers writes until the
  handler returns, and `Flush` on the wrapped writer does not push bytes
  toward an attached reader.
- **Real socket via `httptest.NewServer`** is required for streaming, hijack,
  long-lived, or `Flush`-sensitive endpoints. Used by
  `internal/server/e2etest/` and the existing `TestSSE_*` tests in
  `internal/server/server_test.go`.

Direct handler-function calls — for example, `s.handleSSE(w, r)` — are not
wire-level. They bypass routing and every middleware. They are allowed only
when the test injects a fault into the `http.ResponseWriter` (e.g.,
`SetWriteDeadline` failure) or otherwise probes control flow that cannot be
expressed against a real or simulated wire. Two such tests exist today
(`TestSSE_TerminatesOnInitialDeadlineFailure` and
`TestSSE_TerminatesOnMidStreamDeadlineFailure`); the discipline names this as
the legitimate exception, not a path to avoid.

## When to write what shape

For new code that ships user-visible HTTP behavior:

- **Default to a wire-level test in `internal/server/apitest/`** (recorder
  transport, generated client). The OpenAPI contract is what consumers see,
  and parsing through the generated types catches schema drift the test
  author wouldn't catch with an ad-hoc struct.
- **Use `internal/server/e2etest/` for any streaming, hijack, or
  `Flush`-sensitive endpoint.** SSE, `roborev` proxy streams, future
  WebSocket flows. Real socket is non-negotiable here because the recorder
  collapses the `Flush` timing observable.
- **Use a raw `http.Request` over the recorder transport when the test
  exercises a path the generated client cannot construct**, such as a
  deliberately wrong `Content-Type`, an intentionally malformed body, or
  any preflight failure that only the runtime mutation guard can produce.
- **Direct handler-function calls are allowed only for fault injection on
  the `http.ResponseWriter`** (deadline failures, hijack errors, write
  cancellation simulated by a wrapping writer). Add a comment naming the
  fault being injected; this is the only signal a reader has that the test
  is intentionally non-wire.

For handler-internal helper unit tests (e.g., URL parsing helpers, label
diff functions, capability resolution) — those are fine as plain function
unit tests in `package server` and not in scope for this discipline. The
rule applies to tests of user-visible HTTP behavior, not to tests of
internal helpers that compose into a handler.

## Bug classes this discipline catches

| Bug class | Where wire-level catches it |
|-----------|-----------------------------|
| Time field serialization (`Z` vs `+00:00`) | Parses raw response body; handler-internal tests inspect `time.Time` values before marshaling. |
| Error code missing from OpenAPI doc | apitest/ generated client surfaces unknown status variants and schema mismatches against generated.ErrorModel. |
| Header set in handler but stripped by middleware | Asserts on `resp.Header`, not the handler's `w.Header()` before middleware ran. |
| Status code overridden by middleware | Asserts on `resp.StatusCode`, not the handler's return. |
| Mutation guard short-circuits before handler dispatch | `srv.ServeHTTP` runs the full middleware chain; handler-internal tests calling the handler directly miss this entirely. |
| SSE Content-Type / Cache-Control drift | Real-socket read; recorder may not reflect what a real client sees on a buffered stream. |

## Where the doctrine lives

Extend `context/testing.md` with an "HTTP testing discipline" section that
contains the operational definition, the per-shape guidance, and a short
table mapping bug class to assertion target. Add one bullet to the Testing
section of `CLAUDE.md` linking to that doctrine. The existing testing.md
already covers provider work, race runtime, SQLite fixtures, and
sleep / timer tests; HTTP testing is the matching topical section.

## Worked examples

The discipline ships with three concrete examples. Each demonstrates a
distinct bug class.

### 1. SSE contract pin — paving test in `internal/server/e2etest/`

**Bug class:** middleware silently adds, drops, or rewrites a response
header on a successful streaming response. Handler-internal tests inspect
`w.Header()` before middleware runs and miss this.

**Shape:** real socket via `httptest.NewServer`.

**Mechanics:**

1. Build the test server. Before starting `httptest.NewServer`, pre-broadcast
   a `sync_status` event through `srv.Hub().Broadcast(...)`. The hub caches
   the most recent `sync_status` event and delivers it as the first frame
   on subscribe, removing the broadcast / read race that a non-cached
   first event would introduce.
2. Start `httptest.NewServer(srv)`. `GET /api/v1/events`.
3. Assert response status 200, `Content-Type: text/event-stream`,
   `Cache-Control: no-cache`. Do not assert `Connection` — it is
   hop-by-hop and absent under HTTP/2.
4. Read the first SSE frame from the response body using a `bufio.Scanner`.
   Recognize the frame by the blank-line terminator. The frame must contain
   an `event: sync_status` line and a `data: ` line.
5. Strip the `data: ` prefix from the data line, decode the bytes as JSON
   into a typed struct, and assert the cached payload.

The test demonstrates: the contract pin is the bytes on the wire, not the
handler's return value. If a middleware ever added `Cache-Control: max-age=300`
on all responses (a real and plausible regression), this test fails.

### 2. Mutation guard 415 — paving test in `internal/server/apitest/`

**Bug class:** middleware short-circuits the request before handler dispatch.
The handler is never invoked. A handler-internal test that calls the handler
function directly cannot observe this at all; a recorder-transport test
through `srv.ServeHTTP` does.

**Shape:** in-process via `httptest.NewRecorder` (existing
`roundTripFunc` transport). Uses a raw `http.Request`, not the generated
client (which would set `Content-Type: application/json` automatically and
defeat the test).

**Mechanics:**

1. Build the server fixture. Build a raw `http.Request`: method `POST`,
   path `/api/v1/sync`, body `bytes.NewReader(nil)`, header
   `Content-Type: text/plain`. The body is intentionally empty; `/api/v1/sync`
   accepts zero-body POSTs.
2. Send the request through `srv.ServeHTTP` via `httptest.NewRecorder`.
3. Assert response status 415.
4. Assert the response `Content-Type` header is `application/json`.
   `writeError` in `internal/server/server.go` writes a JSON
   `{"error": "..."}` body via `writeJSON`, which sets that header.
5. Decode the body into `map[string]string` and assert the `error` field
   contains the substring `Content-Type must be application/json`.

The test is a paving example for one specific kind of negative-path test:
the path where the test deliberately violates a precondition the generated
client always satisfies. The accompanying comment names the reason in the
test source.

### 3. Branch-conflict 409 — converted from in-package to workspacetest/

**Bug class:** structured error JSON shape drifts away from the published
OpenAPI contract.

**Existing test:** `TestWorkspaceCreateIssueBranchConflictReturnsTyped409`
in `internal/server/api_test.go`. Today it lives in `package server`, uses
the local `doJSON` helper, decodes the response into an in-package
`rawProblemDetail` struct, and accesses package-internal fixture helpers
(`setupWorkspaceServerFixture`, `runGit`, `testGitSHA`).

**Migration target:** `internal/server/workspacetest/`, which is already a
black-box test package (`package workspacetest`) and already has an
exported-API workspace fixture (its own `setupWorkspaceServerFixture` that
spins up a real git remote and constructs `server.Server` plus the
generated `apiclient.Client`). It is the natural home for migrated
workspace-touching tests; `apitest/` does not have git fixtures and would
require duplicating them.

The migration is not a transport change — both shapes go through
`srv.ServeHTTP` via the recorder transport — but a package-boundary
change:

- The workspacetest/ version cannot import `package server`'s unexported
  helpers or types. It uses the package-local `setupWorkspaceServerFixture`
  (which already lives in `internal/server/workspacetest/fixtures_test.go`)
  and the generated `apiclient.Client` for the request.
- The asserted fields are: status 409, the problem `type` URN, status,
  detail substring, and the two `errors[]` location / value pairs
  (`body.git_head_ref` and `body.suggested_git_head_ref`).
- The response is parsed through the generated client's `ErrorModel` shape
  (see `internal/apiclient/generated/client.gen.go`, type `ErrorModel`).
- The reuse-branch happy-path follow-up (existing in-package test's second
  assertion) becomes a separate test in the migrated file to keep
  scope narrow.

The original in-package test stays in place. The point of the migration is
to show the side-by-side shape, not to replace coverage.

## Lint target

Out of scope. The task allows an optional `make lint-wire`, but a useful
implementation would either:

- Flag direct handler-function calls and require an annotation comment to
  suppress (creates one-time churn on the existing fault-injection tests
  for low ongoing value), or
- Enforce a no-internal-imports rule across apitest/ and e2etest/ (already
  enforced informally by Go's package boundary).

Neither carries its weight relative to the cultural shift the doctrine
itself drives. The discipline section in `context/testing.md` notes that
a future lint can be added when there is measurable churn around the rule.

## Out of scope

- Migrating existing handler-internal tests beyond the one converted
  example.
- Replacing the in-package `setupTestClient` / `doJSON` helpers. They are
  legitimate scaffolding for in-package tests that probe unexported state.
- Changes to provider-specific tests in `internal/platform/<provider>/`.
- Adding a new error model, header, or response shape — the discipline
  documents what to test, not what to ship.

## File touchpoints

- `context/testing.md` — new section.
- `CLAUDE.md` — one bullet in the existing Testing section pointing to the
  new section.
- `internal/server/e2etest/sse_contract_test.go` — new file, SSE contract
  pin paving test.
- `internal/server/apitest/mutation_guard_test.go` — new file, 415
  mutation-guard paving test.
- `internal/server/workspacetest/issue_workspace_conflict_test.go` — new
  file, migrated branch-conflict example.

If a `mockGH` or fixture helper is missing in the chosen test package
relative to what a new test needs, extend the existing
`fixtures_test.go` in that package minimally. Do not introduce a parallel
set of mocks; reuse what `internal/server/apitest/fixtures_test.go`,
`internal/server/e2etest/fixtures_test.go`, and
`internal/server/workspacetest/fixtures_test.go` already provide.

## Acceptance criteria

- `context/testing.md` has an "HTTP testing discipline" section that names
  the two transports, the cultural rule, the fault-injection exception,
  and the bug-class table.
- `CLAUDE.md` has one new bullet under Testing linking to the section.
- The two paving tests pass under `make test`. The mutation-guard test in
  apitest/ also passes under `make test-short`. The SSE contract test in
  e2etest/ follows whatever the existing e2etest/ short-skip behavior is
  for that package (do not introduce a new short-mode skip in the test).
- The migrated branch-conflict test passes under `make test` (workspacetest
  already skips itself under `-short`; the new test inherits that skip).
  The original in-package test continues to pass unchanged.
- `make vet` and `make lint` are clean.
