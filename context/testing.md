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

## Related context

- [`context/provider-architecture.md`](./provider-architecture.md) documents the
  provider package split and checklist for adding providers.
- [`context/platform-sync-invariants.md`](./platform-sync-invariants.md)
  documents provider identity and capability rules for GitHub, GitLab, and
  future providers.
- [`context/github-sync-invariants.md`](./github-sync-invariants.md) documents
  timeline freshness, SHA-sensitive CI, and fallback rules that usually
  determine which tests belong on a GitHub-specific sync change.
