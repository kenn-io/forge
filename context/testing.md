# Testing

Use this document when choosing test boundaries or lanes, changing provider or
HTTP contract tests, or working on race and integration-test architecture. For
everyday Go test construction and commands, also read
[`context/testing-basics.md`](./testing-basics.md).

- Test server constructors that create a Workspace manager must apply their isolated
  test tmux command; never fall back to the host tmux server
  (`internal/server/kata/test_helpers_test.go::newKataTestServer`).
- Persistent-config fixtures run the real config watcher: after a config save, fakes
  must resolve the post-save repository set, tests await the config event, and read
  `srv.cfg` only under `cfgMu` (`internal/server/settings_test.go::setupTestServerWithConfigContent`).
- Integration-tagged top-level tests use the `TestIntegration...` prefix; build tags
  are additive, so the lane's run filter is what avoids rerunning ordinary tests
  (`Makefile::test-integration`).

## Live agent tests

- Shared agent host changes, or adding an agent to `acpSharedAgents`, must pass the opt-in real-agent
  test, which spends model usage and never runs in CI:
  `KENN_FORGE_LIVE_ACP_TESTS=1 KENN_FORGE_LIVE_ACP_AGENTS='<agent command>;...' go test ./internal/workspace/localruntime -run TestLiveACPSharedHost`.

## Live GraphQL validation

- GraphQL query shape changes (structs, fields, aliases, fragments, pagination, nested
  selections) must pass the owning package's live validation before merge:
  `KENN_FORGE_LIVE_GITHUB_TESTS=1 go test ./internal/github ./platform/github -run 'TestLive.*GraphQLQueriesValidateAgainstGitHub' -shuffle=on`.
- CI gives live validation the workflow `GITHUB_TOKEN` only in trusted contexts
  (pushes to `main`, `workflow_dispatch`, same-repository PRs); the general PR Go test
  step gets no token.
- A GraphQL stub must answer each query with only that query's field: `githubv4`
  fails on unexpected keys and sync silently falls back to REST. Assert a value only
  the GraphQL path can produce.

## CI

- Path buckets in `.github/workflows/ci.yml::detect_changes` are runtime-oriented,
  not extension-only. Frontend unit, browser, and e2e jobs also run on backend
  changes, since backend behavior shapes the SPA contract.
- External forks may consume an existing private Playwright image but never receive
  package-write authority; if the image is absent the fork run fails until a trusted
  run publishes it (`.github/workflows/ci.yml:76`).
- A successful CI retry proves recovery, not a fix; identify the cause or keep
  targeted diagnostics for the next occurrence.
- Only `main` saves CI caches; PR caches evict `main`'s from the 10 GB budget
  (`.github/workflows/ci.yml::lint`).
- Bun caches use exact keys; prefix restores poison lockfile-correct installs and
  accumulate every version ever seen (`.github/workflows/ci.yml::build`).
- Host jobs install apt packages through `eclipse-score/apt-install`, not `apt-get`
  or Playwright `--with-deps`, whose mirror fetches can stall for tens of minutes.
- Container fixtures must not fetch OS packages from public mirrors in CI; build
  fixture-only helpers from the repository on an already-required base image
  (`tests/integration/roborev/Dockerfile`).
- Windows CI runs only the packages and tests its job names; a new
  `*_windows_test.go` never runs until a step selects it (`.github/workflows/ci.yml::pty-windows`).
- When a frontend unit run exits without a summary, read the
  `frontend-unit-diagnostics` artifact; if silent again, rerun once with repository
  variable `ACTIONS_RUNNER_DEBUG=true`, collect runner logs, then remove it.

## Dependency versions

- Guard tests must not assert a literal dependency version; Renovate bumps then fail
  lint. Assert the shape that matters and leave cross-file lockstep to a checker
  that reads the current pin (`scripts/check-playwright-version.mjs`).
- Upgrade Vite and Vitest only together through a Vite+ release that bundles both,
  never through independent Renovate updates; Renovate tracks the `vite` alias as
  `@voidzero-dev/vite-plus-core` (`renovate.json:31`).

## Library-owned behavior

- Forge tests must not assert behavior an imported library owns (kit-ui MediaViewer
  sizing, control placement, focus order); test Forge's wiring, URLs, layering, and
  open/close flow (`frontend/tests/e2e/edit-pr-content.spec.ts`).
- Go kit telemetry tests cover only the install identity and event routing Forge
  hands kit (`internal/telemetry/telemetry_kit_test.go`).
- Only Makefile build targets set kit's PostHog key, and kit reports only for
  release-shaped versions; plain `go build`, `go test`, and dev stacks need no
  telemetry opt-out (`Makefile::LDFLAGS`).
- For kwt, drive Forge entry points (checkout creation, refresh, cleanup); do not
  re-test kwt locking, claiming, base-sync, or error classification with hand-built
  requests (`internal/workspace/manager.go::createWorkspaceCheckout`).
- Contextual Kata tests mock the narrow daemon routes and assert Forge integration,
  association controls, refresh gating, and per-task error isolation, not Kata's
  component suite.

## Provider work

- Test provider changes at the boundary where users would notice: provider package
  (normalization, pagination, auth, typed errors, capabilities), config, DB identity,
  server e2e with real SQLite, frontend route helpers; live/container tests only when
  fakes cannot catch API drift.
- Provider container image bumps keep every launch default and baked tag on one
  release (`internal/server/settingstest/gitlab_container_e2e_test.go::TestGitLabContainerE2E`).
- Container fixtures advertise the host-mapped port in canonical provider URLs while
  the service listens on its internal port (`scripts/e2e/gitlab/docker-compose.yml:10`).
- Fleet setup tests inject every host boundary and use temporary homes; they must
  never inspect or mutate the live daemon, service manager, ingress, or credentials
  (`internal/fleetsetup/setup_test.go`).

## Frontend test lanes

Playwright is not a universal requirement for visible fixes; pick the narrowest lane
that observes the behavior's real owner.

- Vitest jsdom tests cover UI-owned data flow, filtering, menus, route-derived state,
  and whole frontend-owned flows that do not depend on real layout.
- Vitest browser tests (`*.browser.svelte.ts`) cover real DOM, native focus and
  keyboard, computed styles, layout, storage, and `matchMedia` without a server.
- Playwright only when the regression depends on the rendered surface: viewport,
  screenshots, clipping, drag/scroll geometry, canvas/xterm, or engine differences.
- Pure Go server tests own backend-produced facts; add full-stack Playwright only
  when that result must also cross a real rendered browser.
- A backend test of the runtime path plus a component or browser test of presentation
  is sufficient; do not add a full-stack test that only replays proven data.
- Mock the API only for frontend-owned behavior or states the seeded server cannot
  produce, via `frontend/src/test/mockApiFetch.ts`; never assert backend-computed
  values through hand-written frontend data.
- Mock Playwright (`frontend/tests/e2e/`) holds frontend-owned flows with intercepted
  APIs, including ACP chat UI; `frontend/tests/e2e-full/` holds flows that must cross
  the built SPA, Go server, persistence, or provider fixtures.
- When a component change alters a rendering contract, sweep both Playwright lanes
  for specs asserting the old contract; mock specs can encode it without appearing
  in the diff.
- Agent lifecycle hooks have no full agent-launch E2E: kit covers config formats,
  Forge tests profile selection, relay, and HTTP handling as separate seams.

### Vitest

- A browser test that mounts a component directly must `import "./app.css"` before
  measuring geometry or asserting styles, or it sees fallback tokens the app never ships.
- Text-dependent layout thresholds shift between CI and developer fonts; assert the
  invariant on either side of a wrap boundary, never which side a width lands on.
- Responsive layout tests await the geometry invariant itself; repeated first-paint
  measurements do not prove ResizeObserver has delivered.
- Verify browser cleanup changes with repeated full runs in the CI Playwright Linux
  image; macOS and single retries miss intermittent disconnects (`frontend/src/test/browserSetup.ts:7`).
- jsdom lacks `ResizeObserver`, `scrollIntoView`, and font loading; stub new primitives
  a kit bump reaches in `frontend/src/test/setup.ts`, not per suite. Focus-trap tests
  install `stubOffsetParent.ts`; synthetic Tab exercises kit's trap only at wrap boundaries.
- Promote a suite to the Node unit inventory only after A/B runs prove identical test
  identities and outcomes (`frontend/vitest.node-files.ts::nodeUnitTestFiles`).
- Effect schedule tests advance `TestClock` from the `it.effect` runtime; a separate
  `ManagedRuntime` keeps the live clock (`frontend/src/lib/stores/roborev/daemon.svelte.test.ts:224`).
- `onUnhandledError` belongs in the root `test` config; keep ignored errors exact by
  message and framework stack frame (`frontend/vite.config.ts:495`).
- Every `@lucide/svelte/icons/<name>` import, and every new dependency reached through
  the excluded kit-ui source barrel, must be in `optimizeDeps.include`; a cold
  optimizer re-bundles mid-run and breaks unrelated browser suites.
- Set CI concurrency for the selected runner; the fallback worker count is not its
  budget. Run unit and browser projects separately, since Vitest rejects their
  differing worker limits in one group (`frontend/vite.config.ts::resolveBrowserTestWorkers`).
- Threaded unit tests must not start a full Vite dev server to exercise plugin
  middleware; native handles can outlive worker teardown (`frontend/src/lib/dev/healthcheckPlugin.test.ts::startServer`).
- Timezone-sensitive tests must not mutate `process.env.TZ` after workers start;
  launch with `TZ` or stub the formatter.

### Playwright

- Both Playwright suites run in Chromium and Firefox; the GitHub App setup suite
  runs once alongside Chromium (`.github/workflows/ci.yml::e2e`).
- Mock Playwright shares one Vite server: cap CI workers at seven, and keep a test to
  two navigations or split it, since the 30 s timeout covers the whole test
  (`frontend/playwright.config.ts::ciWorkers`).
- Playwright CI uses the private `ensure_playwright_image`; keep its Playwright, Bun,
  and Vite+ pins in the recipe, cache only `/usr/local/install/cache`, and
  materialize `node_modules` from the lockfile before invoking baked `vp`.
- Waits must observe the state the next assertion consumes: API reads after an
  optimistic mutation wait for its response; rendered assertions wait for rendered
  state (`frontend/tests/e2e-full/repo-browser.spec.ts:480`).
- Tests that compress browser polling patch the scheduler's timer primitive and
  await the response carrying the expected state (`frontend/tests/e2e-full/ci-dropdown.spec.ts:40`).
- Drain asynchronous route handlers before teardown with `unrouteAll({ behavior: "wait" })`;
  suites using `route.fetch()` proxies use `behavior: "ignoreErrors"`.
- Retries that write to a persistent authority use per-attempt mutation identities
  or reset state; a committed first attempt poisons its retry.
- Give independent boundary scenarios separate tests; a loop shares one timeout and
  hides the failing boundary.
- A full-stack test claiming a user-triggered mutation works must drive the actual
  control; `page.request` proves only the API contract.
- Failure-path fixtures inject the failure at the operation boundary without mutating
  source data that decides whether the asserted row renders (`cmd/e2e-server/main.go`).
- An absence assertion on a collapsed disclosure's inner text is vacuous; assert on
  the data-gated wrapper's `data-testid`.
- Scope diff locators to `.diff-area`: the hidden-but-mounted conversation renders
  review-thread snippets with the same diff markup.
- Pane tab headers are `role="tab"`; use `getByRole("tab", { name })`, since
  `hasText` is not a valid `getByRole` option and matches every tab.
- Sidebar row buttons' accessible names include indicator labels, so page-wide
  button-name queries also match rows; scope chip assertions to the chip's test id.
- Regressions needing a non-loopback listener are opt-in only in the isolated CI
  container; local runs skip before binding, since the fixture exposes mutation and
  runtime endpoints (`frontend/tests/e2e-full/00-workspace-sidebar.spec.ts:172`).

### Full-stack e2e server

- Full-stack e2e runs one run-owned `cmd/e2e-server` binary with the frontend embedded;
  workers inherit its path so signals reach the server, not a `go run` wrapper. A supplied
  binary is externally owned: never rebuilt or removed (`frontend/tests/e2e-full/support/e2eServer.ts::ensureE2EServerBinary`).
- The standalone e2e server runs outside Go's test-only public-API guard; every
  provider client it builds must block remote I/O explicitly (`cmd/e2e-server/main.go::newE2EGraphQLFetcher`).
- E2E provider decorators embed concrete fixtures, not base interfaces; narrowing
  erases optional capability interfaces (`cmd/e2e-server/main_test.go`).
- The fixture provider serves each ID at its seeded route; a scenario that moves a
  repository must forget the moved ID (`internal/testutil/fixture_client.go::FixtureClient.ForgetRepository`).
- Seeded provider fakes return every child family present in seed data; complete
  mirroring deletes DB-only synthetic children (`internal/testutil/fixtures.go::SeedFixtures`).
- e2e-server fixture Git reads the developer's global config (commits may sign or run
  hooks) under a 60 s readiness timeout; build bulk commits with `git fast-import`.
- Markdown video specs use a committed synthetic VP9 WebM clip on a dedicated e2e
  server, since its seeded items change other specs' pages (`cmd/e2e-server/markdown_video.go`).
- Browser-tier specs that need a load to hold use `/__browser-test/never-responds`
  (`frontend/vite.config.ts`).
- Archive scenarios seed persisted coverage and read the production HTTP export;
  response stubs cannot prove consumers handle missing coverage (`cmd/e2e-server/archive_context.go`).

### Terminals and tmux in browser tests

- Real-tmux tests observe state through the per-instance socket; never replace global
  key bindings, which leak into developer sessions.
- Terminal input tests wait for the session's `claim_resize` before typing
  (`frontend/tests/e2e-full/support/terminalReadiness.ts::waitForTerminalInputReady`).
- Retry asynchronous resize probes on a bounded timer, never per repaint; repaint
  coupling can overflow subscriber buffers.
- Terminal-readiness assertions are renderer-agnostic (`canvas, .xterm-screen`):
  headless Firefox lacks WebGL and xterm.js falls back to a DOM renderer.
- Derive xterm pointer coordinates from rendered cell geometry, not WebSocket
  dimensions; canvas link tests re-enter the target cell while polling.
- Moving a session between hosts: close the old WebSocket before mounting the next
  and reassert mouse mode; concurrent clients change tmux sizing and modes.
- Reconnect tests close and observe the WebSocket before enabling offline mode.
- Await `replay_ready` when testing terminal replay.
- Terminal-protocol negotiation tests use the PTY-owner e2e option; tmux consumes
  application mode sequences.
- Clipboard-race tests emit OSC 52 through the attached tmux client and assert the
  socket observed it before trusting ordering.
- Keep focus-click selection and delayed outside-copy revocation as separate
  regressions; the delayed case keeps terminal DOM focus so `focusout` cannot mask a bug.
- PTY-owner quick-exit coverage asserts the exact nonzero status through the
  HTTP-launched WebSocket and SQLite cleanup; accepting unknown `-1` proves nothing.

### Roborev

- `hide_classify_jobs` fixtures cover skipped design rows and classify-typed
  auto-design rows; seed classify rows terminal unless testing worker mutation.
- Real Roborev mutation fixtures need an available fixture agent; pause the queue to
  assert a queued rerun (`--workers 0` keeps the default count).
- Keep injected `panel_run` failures in place until the assertion observes them;
  refresh demand retries member fetches immediately.
- Roborev `/api/stream/events` is NDJSON, not SSE.
- The `e2e-roborev` daemon pin (`ROBOREV_REF`) must be at or after the commit the
  generated Roborev schema came from; a stale pin silently omits newer fields.
- Managed-clone initialization must exercise the pinned real Roborev CLI and daemon;
  a fake hook writer cannot validate linked-worktree registration.
- The Docker reboot simulation needs real tmux, so it stays an opt-in local target
  outside CI; its fake agent must send hook payloads the pinned decoder accepts.

## Huma API contract

- Every public operation needs explicit registration-site metadata: stable kebab-case
  `OperationID`, short imperative `Summary`, and exactly one taxonomy tag
  (`internal/server/settingstest/route_metadata_test.go`).
- Never rely on Huma's generated summary or operation ID; an `OperationID` change is
  a generated-client API change even when the path is unchanged.
- After route metadata changes, run `TestHumaContractMetadata|TestRouteMetadataWalker`
  and `make api-generate`, then review client diffs for renames.
- Generated Go clients encode path and query parameters through `paramcodec.Map`,
  never `runtime.AsMap`, which sends large IDs in exponent form; test new integer
  parameters with real-size values.
- Local client-with-response templates copy upstream oapi-codegen-dd; a generator bump
  must rebase them and regenerate in the same change.
- The local header template omits `github.com/google/uuid`; keep it out when rebasing,
  since `format: uuid` maps to stdlib `uuid.UUID`.
- Health routes disable OpenAPI. Terminal and proxy routes from `Adapter().Handle`
  stay hidden unless promoted to public REST operations with full metadata.
- The transport inventory must come from Huma registration metadata, never a
  hand-maintained route snapshot (`go run ./cmd/kenn-forge-transport-inventory`).
- Do not duplicate e2e tests across default-host and `/host/{platform_host}` routes
  unless the change touches host logic, route parsing, or provider identity.

## Race test runtime

Treat `go test -race` runtime as test architecture, not a CI-only concern.

- Keep large black-box flows in separate test packages so they build as separate
  race binaries; tests needing unexported internals stay in the source package.
- Replace fixed sleeps with explicit events, callbacks, or readiness channels.
- Prepare SQLite fixtures before starting a subprocess timeout; process-local
  template caches are cold after exec.
- Add `t.Parallel()` only after proving the test touches no process-global state,
  fixed external resources, shared tmux sessions, or shared database files.
- Default new full-stack server tests that own their temp dirs, SQLite, fakes, and
  server to `t.Parallel()`; stay serial for `t.Setenv`, process-global state, fixed
  ports, or shared resources.
- Real tmux and PTY tests stay parallel when each owns its sessions, sockets, and
  dirs; gate resource pressure with a package `semaphore.Weighted`, not serialization.
- Use `make race-times` and the CI timing artifacts for regressions, not local guesses.
- Go CI shards stay exhaustive (server descendants, core, rest); live GraphQL and
  integration suites run once in `core` (`.github/workflows/ci.yml::test_go`).
- DB-backed server fixtures drain `Server.Shutdown` before SQLite `t.TempDir` removal
  (`internal/testutil/servertest/servertest.go::New`).
- Windows test binaries launching restart-durable PTY processes contain descendants
  in a kill-on-close Job Object (`internal/testutil/processjob/processjob_windows.go`).

### Test tmux lifecycle

- E2e processes stop private tmux servers before slower graceful shutdown; Go defers
  and Node `exit` callbacks cannot clean up after forced termination (`cmd/e2e-server/main.go::appState.stopTmux`).
- Once e2e-server tmux shutdown starts, admit no new session; cleanup waits for an
  admitted creation (`cmd/e2e-server/main.go::tmuxCreationGate`).
- Keep explicit PTY-owner test mode unwrapped so its missing tmux command stays
  unavailable to backend selection (`cmd/e2e-server/main.go::buildAppState`).
- Playwright workers publish child ownership in one shared run-owned socket root,
  which stays until every child exits; terminating children can still daemonize tmux.
- Stale recovery touches only tmux roots, owner files, and sockets owned by the
  current user; a matching test name is not ownership.
- Real-tmux Go test binaries install signal cleanup (`internal/testutil/testsignal.Install`);
  long-lived package-level servers use `testtmux.Owner` for uncatchable owner death.

### Concurrency and boundary contracts

- Prove multi-query snapshots at the service transaction boundary; query-only tests
  cannot expose torn reads.
- Prove pause by blocking provider I/O, pausing, releasing, then asserting no commit.
- Prove worker readiness, wake, and shutdown through the owning loop, not direct calls.
- Prove cross-ingestion child replacement with a newer dataset then an older complete
  replacement; parent-only races miss destructive child deletes.
- Prove lock ownership with a `TryLock` probe while the owner is parked inside the
  section; an event emitted before `Lock` proves nothing.
- Exercise persistence at exact advertised record ceilings, including repeated
  replacement; small fixtures miss SQLite bind and batch limits.
- Inclusive-watermark refreshes reopen every equal-timestamp observation; equality
  cannot prove stasis without a revision or fingerprint.
- Snapshot race coverage includes equal provider timestamps through a real sync and
  generated HTTP client.

### SQLite fixtures

- Ordinary DB tests use the copied-template fixture (`internal/testutil/dbtest.Open`,
  or `openTestDB` inside `internal/db`); migration, repair, and schema-history tests
  use real migrations (`dbtest.OpenWithMigrationsAt`, `openDBWithMigrations`).
- Outside `internal/workspace`, build workspace managers with
  `internal/testutil/workspacetest.NewManager`; do not copy a local helper.

### Sleeps and timers

- Do not synchronize with sleeps. Await a channel closed by the observer of the exact
  event; if only polling can observe it, check immediately, then tick under a deadline.
- Async cache tests observe the baseline through the consumer before mutating inputs
  or advancing a fake clock.
- Workspace enrichment clocks advance through failed attempts while polling; the
  retry TTL uses the same clock.
- Chained ad-hoc sync triggers wait for `/api/v1/sync/status` `running=false` with a
  `last_run_at`; `TriggerRun` is single-flight and skips overlapping triggers.
- Production timeouts exercised by tests need headroom over scheduling noise (they
  bound dialling too); await the fake's observation rather than reading a counter.
- Workspace fixtures with background monitors seed observer inputs before `server.New`.
- Use `testing/synctest` only for pure in-process goroutines and timers, never around
  `httptest.Server`, WebSockets, tmux, PTYs, git, subprocesses, external filesystem
  polling, or `t.Run`/`t.Parallel`/`t.Deadline` inside the bubble.
- Outside synctest bubbles, kennlint rejects sub-second constant wait budgets and
  `time.Sleep`. `//nolint:kennlint // reason` is only for a deadline that is the
  asserted result, an absence check, a real external wait, or a fake-time helper
  called only in a bubble, naming what holds it. Short positive `Never` windows are allowed.
- Tests redirecting process-wide `slog` output use a concurrency-safe writer;
  background monitors log after their test.

## HTTP testing discipline

A wire-level test sends the request through `srv.ServeHTTP` and asserts only what a
client observes: status, headers, and body bytes.

- API request/response tests go in `internal/server/apitest/` over
  `httptest.NewRecorder` with the generated client; `generated.ErrorModel` catches
  OpenAPI drift.
- Streaming, hijack, and `Flush`-sensitive endpoints need `httptest.NewServer`
  (`internal/server/e2etest/`); the recorder buffers.
- Inputs the generated client cannot produce use a raw `http.Request`; comment why.
- Direct handler-function calls skip routing and middleware; allowed only to inject
  a `ResponseWriter` fault, commented.
- Handler-internal helper unit tests stay plain unit tests in `package server`.
- Test cleanup must not delete a failing test that still covers repository-owned
  behavior; remove only obsolete assertions or those covered at the same boundary.
- Mirrored frontend implementations of one contract run the owner-level contract
  against each (`frontend/src/lib/stores/comment-mutation-contract.ts`).
- TypeScript/Svelte cleanup adds tests only when behavior, cross-module contracts, or
  regression risk change; prefer typecheck and lint over fabricated fake boundaries.

## Related context

- [`context/provider-architecture.md`](./provider-architecture.md): provider
  package split and checklist for adding providers.
- [`context/platform-sync-invariants.md`](./platform-sync-invariants.md): provider
  identity and capability rules.
- [`context/github-sync-invariants.md`](./github-sync-invariants.md): rules that
  usually decide which tests belong on a GitHub sync change.
