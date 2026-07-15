# Testing

## Go assertion style

Go tests use `testify` consistently. Import
`github.com/stretchr/testify/assert` without an alias and create local helpers
as `assert := assert.New(t)` when a test has more than three assertions.
Aliasing the `assert` package, including `Assert`, is not allowed and is
enforced by golangci-lint's `importas` rule.

## Live GraphQL validation

GraphQL query shape changes must be validated against GitHub's live GraphQL API before they are merged. The local test suite includes a gated live test:

```sh
MIDDLEMAN_LIVE_GITHUB_TESTS=1 go test ./internal/github -run TestLiveGraphQLQueriesValidateAgainstGitHub -shuffle=on
```

The test uses `MIDDLEMAN_GITHUB_TOKEN` first, then `GITHUB_TOKEN`. It intentionally skips unless `MIDDLEMAN_LIVE_GITHUB_TESTS=1` is set because live validation consumes GitHub GraphQL rate limit and requires network access.

When changing structs, fields, aliases, fragments, pagination arguments, or nested selections used by `internal/github/graphql.go`, enable `MIDDLEMAN_LIVE_GITHUB_TESTS=1` and run the live validation test in addition to the normal Go tests.

CI runs the live GraphQL validation as a separate Go test step using the workflow `GITHUB_TOKEN` only in trusted contexts, such as pushes to `main`, manual `workflow_dispatch` runs, and same-repository pull requests. The general pull request Go test step does not receive a GitHub token.

## CI path-gated test jobs

The CI workflow classifies changed paths once in `.github/workflows/ci.yml::detect_changes`
and uses that result to gate expensive test jobs. Keep the path buckets
runtime-oriented rather than extension-only: the backend bucket includes Go
files, Go modules, migrations, generated API client inputs, embedded web
assets, and integration fixtures; the Rust bucket includes root Cargo manifests
and the Rust workspace; the e2e bucket includes Playwright config, e2e tests,
scripts, and integration fixtures. Frontend unit, browser, and Playwright e2e
jobs run when either frontend paths change or backend paths change, because
backend/API behavior can affect the SPA contract even when no TypeScript files
moved. Manual `workflow_dispatch` forces all buckets on so maintainers can
request a full test pass.

## Dependency versions in guard tests

Guard tests must not assert a literal dependency version (for example
`assert.equal(pkg.devDependencies["@playwright/test"], "1.61.0")`); every
Renovate bump then fails lint until someone hand-edits the test. Assert the
shape that actually matters (exact pin, correct source of the dependency) and
leave cross-file version lockstep to a dedicated checker such as
`scripts/check-playwright-version.mjs`, which reads the current pin instead of
hardcoding one.

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

Provider container fixture image bumps must keep every launch default and baked
image tag on one release, or local e2e and bake paths can test different
provider versions (`internal/server/gitlab_container_e2e_test.go::TestGitLabContainerE2E`).

Regenerate OpenAPI and generated clients with `make api-generate` after Huma
route or API type changes.

## Frontend test lane selection

Do not treat Playwright or full-stack e2e as a universal "must have" for every
visible frontend fix. Pick the narrowest lane that observes the behavior's real
owner:

- Use Vitest component/store tests for UI-owned data flow, filtering, sorting,
  hidden/disabled states, menu contents, route-derived view state, and app-shell
  behavior that does not depend on real browser layout.
- Use Vitest browser tests (`*.browser.svelte.ts` with `vitest-browser-svelte`)
  when the behavior needs a real browser DOM, native focus or keyboard behavior,
  computed styles, layout, localStorage, matchMedia, or other browser primitives,
  but does not need an external server or Playwright navigation flow.
- Use Playwright mock e2e when the regression is specifically about a
  multi-step browser workflow, viewport behavior, screenshots/video, drag,
  scroll/sticky/overflow geometry, canvas/xterm rendering, or browser navigation.
- Use full-stack e2e or server/API tests when the disputed fact is produced by
  backend persistence, sync, capabilities, normalization, route middleware, or
  wire serialization.

A UI regression can be sufficiently covered by a backend/server test for the
real runtime path plus a component or Vitest browser test for presentation. Do
not require a duplicate full-stack browser test when it would only replay data
that is already proven at those two boundaries.

Every `@lucide/svelte/icons/<name>` import added anywhere in `frontend/src` or
`packages/ui/src` must also be added to the `optimizeDeps.include` list in
`frontend/vite.config.ts`. A missing entry passes locally on a warm Vite cache
but fails the browser-lane CI job on a cold one: the optimizer discovers the
icon mid-run, re-bundles, and the page reload breaks unrelated suites with
"Failed to fetch dynamically imported module". Verify with the grep documented
above that list in the config.

Vitest owns unhandled errors in the root runner, so `onUnhandledError` belongs
in the root `test` config rather than a browser project; keep any ignored error
exact by message and framework stack frame (`frontend/vite.config.ts:495`).

Full-stack e2e serves the frontend embedded in the e2e-server binary
(`internal/web/dist`), not live sources: run `make frontend` before building
`cmd/e2e-server` locally, or the suite silently validates a stale bundle and
passes on frontend changes that CI then fails.

Mounting `KataWorkspace.svelte` in Vitest always fetches the daemon roster and
opens the live SSE event stream; mock both fetch routes (see
`mockDaemonAndStreamFetch` in `KataWorkspaceEventStream.test.ts`) or the
stream's error recovery reloads the view mid-test and silently reverts
selections made by the test.

Roborev `hide_classify_jobs` e2e fixtures must cover skipped design rows and classify-typed auto-design rows.
Seed classify rows terminal unless testing worker mutation; live workers can rewrite queued/running rows during browser assertions (`internal/testutil/roborev_fixtures.go::seedRoborevMutationFixtures`).
Keep injected Roborev `panel_run` failures controlled until the assertion observes them; drawer/list refresh demand can immediately retry member fetches and clear transient panel errors (`packages/ui/src/stores/roborev/jobs.svelte.ts::wantsPanelMembers`).
The `e2e-roborev` daemon pin (`ROBOREV_REF` in `.github/workflows/ci.yml`) must be at or after the roborev commit the generated schema (`packages/ui/src/api/roborev/generated/`) was produced from; a stale pin makes the daemon silently omit newer response fields while seed inserts still succeed, because the seeder creates its own schema.

Kata reachable-graph tests can use `window.__middleman_kata_graph_debug` in
browser/e2e runs, or `kata-graph-debug.ts` directly in unit tests, to inspect
recent graph/store events and the latest rendered node IDs. Prefer this bridge
when debugging or asserting graph refresh ordering, missing-ref population,
selection detail refreshes, or node/edge stability; it avoids brittle visual
polling for state that the app already owns in JavaScript. Browser/e2e tests
should call `reset()` before assertions, and the graph component clears the
bridge on unmount so assertions do not read stale graph sessions.

Playwright waits must observe the rendered state consumed by the next assertion,
not only the request completion or control value that triggered it; route refinement
can leave new controls paired with old or loading content (`frontend/tests/e2e-full/kata.spec.ts:1625`, `frontend/tests/e2e-full/repo-browser.spec.ts:480`).

CI executes both Playwright suites in Chromium and Firefox. Frontend-owned
browser workflows using intercepted API responses belong in
`frontend/tests/e2e/` (`frontend/playwright.config.ts`); workflows that must
cross the built SPA, Go server, middleware, persistence, or provider fixture
boundaries belong in `frontend/tests/e2e-full/`
(`frontend/playwright-e2e.config.ts`).

A full-stack test claiming a user-triggered mutation works must drive the actual
control and observe its request or visible result; `page.request` proves only the
API contract (`frontend/tests/e2e-full/detail-action-buttons.spec.ts:925`).

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

New full-stack server tests should default to `t.Parallel()` when they build
their own `t.TempDir` filesystem, SQLite fixture, provider fake, and server
fixture. This especially applies to workspace/git e2e coverage where each test
clones its own bare repo and worktree root. Keep tests serial when they call
`t.Setenv`, mutate process-global state, rely on fixed ports or shared external
resources, or intentionally verify ordering against another test-visible shared
resource.

Disable Git auto-GC and auto-maintenance in synthetic repositories under
`t.TempDir`; detached maintenance can recreate files during fixture cleanup
(`internal/gitclone/commits_test.go::commitTestRun`).

Real tmux and PTY tests can still run in parallel when each test owns its
session names, temp dirs, sockets, and cleanup. If the bottleneck is external
resource pressure rather than correctness, keep `t.Parallel()` and gate the
expensive section with a package-level `golang.org/x/sync/semaphore.Weighted`
instead of serializing the whole test.

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

When server e2e tests chain `POST /api/v1/sync` with another ad-hoc sync
trigger, treat the HTTP 202 and DB row timestamps as intermediate observations.
`TriggerRun` is non-blocking and single-flight; wait for `/api/v1/sync/status`
to report `running=false` with a `last_run_at` before issuing the next trigger,
or the next trigger can race the still-running sync and be skipped.

Workspace fixtures with background monitors must seed observer inputs before
`server.New`; the pushed-head observer runs an immediate first pass and can
capture transitional fixture state (`internal/server/server.go::runWorkspacePushedHeadObserverLoop`).

`testing/synctest` is appropriate only when all goroutines and timers under test
are pure in-process work created inside the `synctest.Run` bubble. Good
candidates include fake-client backoff, cooldown, cancellation, and event-hub
tests. Do not use `synctest` around `httptest.Server`, WebSockets, tmux, PTYs,
git, shell commands, filesystem polling driven by external processes, or tests
that call `t.Run`, `t.Parallel`, or `t.Deadline` inside the bubble.
`synctest.Wait` is race-detector synchronization, so it is useful under
`go test -race` when the test is structurally eligible.

## HTTP testing discipline

A test of user-visible HTTP behavior is **wire-level** when the request flows
through `srv.ServeHTTP` (every middleware runs) and assertions read what a
client observes: status, response headers, and body bytes. The handler
function's return value is not consulted.

Two transports qualify:

- **`httptest.NewRecorder`** is the default for request/response tests, used
  by `internal/server/apitest/`. No `net.Conn`, so writes buffer until the
  handler returns and `Flush` does not push toward a reader — the recorder
  cannot honor streaming or hijack semantics.
- **`httptest.NewServer`** is required for streaming, hijack, or
  `Flush`-sensitive endpoints. Used by `internal/server/e2etest/` and the
  in-package `TestSSE_*` tests.

Defaults for new code:

- API request/response → `internal/server/apitest/` with the generated client;
  decoding through `generated.ErrorModel` catches OpenAPI drift.
- Streaming, hijack, `Flush`-sensitive → `internal/server/e2etest/`.
- Inputs the generated client cannot produce (wrong `Content-Type`, malformed
  body, preflight failure) → raw `http.Request` over the recorder; comment
  the reason.
- Direct handler-function calls (`s.handleSSE(w, r)`) skip routing and
  middleware. Allowed only to inject a fault into the `http.ResponseWriter`;
  comment the fault. The two `TestSSE_Terminates...` deadline tests are the
  legitimate exception.

For TypeScript/Svelte cleanup, add tests only when user-visible behavior,
cross-module contracts, or an actual regression risk changes. Do not fabricate
large fake browser boundaries, console-spy cases, or component tests merely to
justify replacing assertions with better types. If the change is a local type
contract improvement and existing interaction tests still cover the behavior,
prefer focused typecheck/lint validation over new test scaffolding.

Handler-internal helper unit tests (URL parsing, label diffs, capability
resolution) stay as plain unit tests in `package server` and are out of
scope.

Bug classes wire-level catches:

| Bug class                                     | Why the wire matters                                 |
| --------------------------------------------- | ---------------------------------------------------- |
| Time serialization (`Z` vs `+00:00`)          | Response bytes, not `time.Time` before marshaling.   |
| Error missing from OpenAPI doc                | Generated client surfaces unknown status variants.   |
| Header rewritten or stripped by middleware    | `resp.Header`, not `w.Header()` inside the handler.  |
| Status overridden by middleware               | `resp.StatusCode`, not the handler return.           |
| Mutation guard short-circuits before dispatch | Only `srv.ServeHTTP` runs the full middleware chain. |
| SSE `Content-Type` / `Cache-Control` drift    | Real-socket read; the recorder is buffered.          |

Worked examples: `internal/server/e2etest/sse_contract_test.go` pins SSE
headers and the first cached `sync_status` frame;
`internal/server/apitest/mutation_guard_test.go` asserts the 415 response
for `POST Content-Type: text/plain`;
`internal/server/workspacetest/issue_workspace_conflict_test.go` reproduces
a 409 through `generated.ErrorModel` alongside the in-package original.

## Related context

- [`context/provider-architecture.md`](./provider-architecture.md) documents the
  provider package split and checklist for adding providers.
- [`context/platform-sync-invariants.md`](./platform-sync-invariants.md)
  documents provider identity and capability rules for GitHub, GitLab, and
  future providers.
- [`context/github-sync-invariants.md`](./github-sync-invariants.md) documents
  timeline freshness, SHA-sensitive CI, and fallback rules that usually
  determine which tests belong on a GitHub-specific sync change.
