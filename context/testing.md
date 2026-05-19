# Testing

## Live GraphQL validation

GraphQL query shape changes must be validated against GitHub's live GraphQL API before they are merged. The local test suite includes a gated live test:

```sh
MIDDLEMAN_LIVE_GITHUB_TESTS=1 go test ./internal/github -run TestLiveGraphQLQueriesValidateAgainstGitHub -shuffle=on
```

The test uses `MIDDLEMAN_GITHUB_TOKEN` first, then `GITHUB_TOKEN`. It intentionally skips unless `MIDDLEMAN_LIVE_GITHUB_TESTS=1` is set because live validation consumes GitHub GraphQL rate limit and requires network access.

When changing structs, fields, aliases, fragments, pagination arguments, or nested selections used by `internal/github/graphql.go`, enable `MIDDLEMAN_LIVE_GITHUB_TESTS=1` and run the live validation test in addition to the normal Go tests.

CI runs the live GraphQL validation as a separate Go test step using the workflow `GITHUB_TOKEN` only in trusted contexts, such as pushes to `main`, manual `workflow_dispatch` runs, and same-repository pull requests. The general pull request Go test step does not receive a GitHub token.

## Provider work

When adding or changing a provider, pick tests at the boundary where users would
notice the regression:

- provider package tests for API normalization, pagination, auth/header shape,
  typed platform errors, and capability flags;
- config tests for provider defaults, host normalization, nested paths,
  duplicate detection, and token selection;
- DB/query tests for provider-aware identity and provider ID reconciliation;
- server e2e tests with real SQLite for route payloads, settings/import flows,
  and capability-gated actions;
- frontend store/component tests for provider refs and generated route helpers;
- optional live/container tests when fakes cannot validate provider API drift.

Regenerate OpenAPI and generated clients with `make api-generate` after Huma
route or API type changes.

## Huma API Contract

Every public operation in `/api/v1/openapi.json` must have explicit OpenAPI
metadata at the route registration site:

- stable kebab-case `OperationID`;
- short imperative `Summary`;
- exactly one tag from the API tag taxonomy enforced in
  `internal/server/route_metadata_test.go`.

Use `documentOperation(...)` for Huma convenience helpers such as `huma.Get`
and `huma.Post`. Use inline `Summary`, `Tags`, and `OperationID` fields for
`huma.Register` blocks. Do not rely on Huma's generated summary or operation
ID; those names feed checked-in generated clients, so changing an
`OperationID` is a generated-client API change even when the HTTP path is
unchanged.

Health routes on the separate health Huma API intentionally disable OpenAPI and
docs output. Terminal and proxy routes registered through `Adapter().Handle`
must stay hidden or on a docs-disabled API unless they are promoted to public
REST operations with the same metadata and generation workflow.

For route metadata changes, run:

```sh
go test ./internal/server -run 'TestHumaContractMetadata|TestHumaConvenienceRoutesUseDocumentOperation|TestRouteMetadataWalker' -shuffle=on
make api-generate
```

Then review generated Go and TypeScript client diffs for operation-name
renames and update checked-in callers that use generated method/type names.

Do not duplicate full-stack e2e tests across default-host and
`/host/{platform_host}` route forms when the host route is only a generic
wrapper. Add host-specific e2e coverage only for custom host logic, route
parsing, or provider identity changes.

## Race test runtime

Treat `go test -race` runtime as a test architecture concern, not a CI-only
concern. The main levers are:

- keep large black-box flows in separate test packages so Go can schedule them
  as separate race test binaries;
- replace fixed sleeps with explicit events, callbacks, readiness channels, or
  short polling loops that check immediately before waiting;
- reuse migrated SQLite template databases for isolated non-migration tests;
- add `t.Parallel` only after proving the test does not touch process-global
  state, fixed external resources, shared tmux sessions, or shared database
  files.

Use `make race-times` to get a local package timing baseline for the current
slow packages. CI also writes race timing JSON and summarizes slow packages and
tests in the `go test -race` job summary. When a PR regresses race runtime, use
the CI timing artifact rather than guessing from local timings alone.

Keep splitting new high-volume tests into the existing black-box packages when
they do not need unexported internals:

- `internal/server/apitest` for HTTP API behavior through the generated client;
- `internal/server/workspacetest` for workspace, runtime, terminal, and
  tmux-heavy HTTP flows;
- `internal/github/syncertest` for exported syncer contract behavior;
- `internal/db/projecttest` for project-package DB behavior that can avoid the
  core `internal/db` package.

Leave tests in the source package when they exercise unexported helpers,
migration state, dirty database handling, or other internal invariants.

### SQLite Fixtures

Use the copied-template database fixture for ordinary DB-backed tests that only
need a fresh migrated schema:

- outside `internal/db`, prefer `internal/testutil/dbtest.Open(t)`;
- inside `internal/db`, use the package-local `openTestDB(t)` from
  `fixture_test.go`;
- keep migration, legacy repair, dirty migration, and schema-history tests on
  `dbtest.OpenWithMigrationsAt(t, path)`, `db.Open`, or the package-local
  `openDBWithMigrations(t)`.

The template fixture migrates once, checkpoints WAL, copies the database file
into each test's `t.TempDir`, and opens the copy with `OpenPreparedForTest`.
That preserves per-test isolation without paying migration setup for every
fixture.

### Sleep And Timer Tests

Do not add sleeps as a synchronization mechanism. Prefer a channel closed by
the fake or callback that observed the exact event under test. If the behavior
is inherently observable only by polling, check once immediately, then poll with
a short ticker bounded by a context deadline.

`testing/synctest` is appropriate only when all goroutines and timers under test
are pure in-process work created inside the `synctest.Run` bubble. Good
candidates include fake-client backoff, cooldown, cancellation, and event-hub
tests. Do not use `synctest` around `httptest.Server`, WebSockets, tmux, PTYs,
git, shell commands, filesystem polling driven by external processes, or tests
that call `t.Run`, `t.Parallel`, or `t.Deadline` inside the bubble.
`synctest.Wait` is race-detector synchronization, so it is useful under
`go test -race` when the test is structurally eligible.

## HTTP testing discipline

A test of user-visible HTTP behavior is **wire-level** when both of the
following hold:

1. The request flows through `srv.ServeHTTP`, so every middleware the
   production server installs runs against the test request.
2. Assertions read the response a client would actually observe: status
   code, response headers, and response body bytes. The handler
   function's return value is not consulted.

Two transports satisfy this definition:

- **In-process via `httptest.NewRecorder`** is the default for
  request / response tests. Used by `internal/server/apitest/` and the
  in-package `doJSON` helper. Fast, no port allocation, deterministic.
  Fires every middleware. Does not faithfully simulate streaming I/O:
  there is no `net.Conn` behind the recorder, the recorder buffers
  writes until the handler returns, and `Flush` on the wrapped writer
  does not push bytes toward an attached reader.
- **Real socket via `httptest.NewServer`** is required for streaming,
  hijack, long-lived, or `Flush`-sensitive endpoints. Used by
  `internal/server/e2etest/` and the in-package `TestSSE_*` tests in
  `internal/server/server_test.go`.

Direct handler-function calls (for example, `s.handleSSE(w, r)`) are not
wire-level. They bypass routing and every middleware. Allow them only
when the test injects a fault into the `http.ResponseWriter` itself
(deadline failures, hijack errors, write cancellation simulated by a
wrapping writer) or otherwise probes control flow that cannot be
expressed against a real or simulated wire. The two existing tests of
this shape (`TestSSE_TerminatesOnInitialDeadlineFailure` and
`TestSSE_TerminatesOnMidStreamDeadlineFailure`) are the legitimate
exception, not a path to avoid.

For new code that ships user-visible HTTP behavior:

- Default to a wire-level test in `internal/server/apitest/` (recorder
  transport, generated client). The OpenAPI contract is what consumers
  see, and parsing through the generated types catches schema drift the
  test author would not catch with an ad-hoc struct.
- Use `internal/server/e2etest/` for any streaming, hijack, or
  `Flush`-sensitive endpoint. SSE, the roborev proxy streams, and any
  future WebSocket flow belong here. Real socket is non-negotiable
  because the recorder collapses the `Flush` timing observable.
- Use a raw `http.Request` over the recorder transport when the test
  exercises a path the generated client cannot construct, such as a
  deliberately wrong `Content-Type`, an intentionally malformed body,
  or any preflight failure that only the runtime mutation guard can
  produce. Add a comment naming the reason; this is the only signal a
  reader has that the test is intentionally not using the generated
  client.
- Direct handler-function calls are allowed only for fault injection on
  the `http.ResponseWriter`. Add a comment naming the fault being
  injected.

Handler-internal helper unit tests (URL parsing helpers, label diff
functions, capability resolution) are fine as plain function unit tests
in `package server` and are not in scope. The rule applies to tests of
user-visible HTTP behavior, not to tests of internal helpers that
compose into a handler.

The bug classes wire-level tests catch:

| Bug class | Assertion target |
|-----------|------------------|
| Time field serialization (`Z` vs `+00:00`) | Raw response body; handler-internal tests inspect `time.Time` values before marshaling. |
| Error code missing from OpenAPI doc | `apitest/` generated client surfaces unknown status variants and schema mismatches against `generated.ErrorModel`. |
| Header set in handler but stripped by middleware | `resp.Header`, not the handler's `w.Header()` before middleware ran. |
| Status code overridden by middleware | `resp.StatusCode`, not the handler's return. |
| Mutation guard short-circuits before handler dispatch | `srv.ServeHTTP` runs the full middleware chain; handler-internal tests calling the handler directly miss this entirely. |
| SSE Content-Type / Cache-Control drift | Real-socket read; the recorder does not faithfully simulate what a real client sees on a buffered stream. |

Three worked examples ship the discipline:

- `internal/server/e2etest/sse_contract_test.go` pins the SSE response
  headers and first cached `sync_status` frame on the wire.
- `internal/server/apitest/mutation_guard_test.go` sends a raw `POST`
  with `Content-Type: text/plain` and asserts the 415 response shape.
- `internal/server/workspacetest/issue_workspace_conflict_test.go`
  reproduces an in-package 409 test as a black-box example that decodes
  through `generated.ErrorModel`. The original in-package test stays in
  place.

A `make lint-wire` target is intentionally out of scope. Either it
would flag the legitimate fault-injection tests and require annotation
comments to suppress (one-time churn for low ongoing value), or it
would enforce a no-internal-imports rule already enforced informally by
Go's package boundary. A future lint can be added if there is
measurable churn around the rule.

## Related context

- [`context/provider-architecture.md`](./provider-architecture.md) documents the
  provider package split and checklist for adding providers.
- [`context/platform-sync-invariants.md`](./platform-sync-invariants.md)
  documents provider identity and capability rules for GitHub, GitLab, and
  future providers.
- [`context/github-sync-invariants.md`](./github-sync-invariants.md) documents
  timeline freshness, SHA-sensitive CI, and fallback rules that usually
  determine which tests belong on a GitHub-specific sync change.
