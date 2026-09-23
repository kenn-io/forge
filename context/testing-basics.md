# Test Basics

Use this document when writing or running Go tests, choosing common test
fixtures, or changing shell-script coverage.

- Pass `-shuffle=on` when invoking `go test` directly; `make test` and
  `make test-short` already include it. Do not pass redundant `-count=1`; use
  `-count=N` only when `N > 1` for repeated runs.
- Before pushing Go changes, run `make nilaway`, `make fmt-check`, and
  `make lint-check`. Lint uses kit's shared policy through `./custom-gcl`;
  `golangci-lint run` does not apply v2 formatters, so formatter drift is a
  separate gate (`.github/workflows/ci.yml::lint`, `Makefile::fmt-check`).
- `t.Context()` is already canceled inside `t.Cleanup`; shutdown helpers use
  `context.WithoutCancel(t.Context())` or `context.Background()`
  (`internal/testutil/servertest/servertest.go::registerCleanup`).
  Unix-socket fixtures keep a short `/tmp` root rather than `t.TempDir()`.
- Direct Make test lanes bound package concurrency (`GO_TEST_P=` restores
  native concurrency). Go hooks run uncapped and the read-only Go consumers run
  concurrently after `golangci-lint --fix`; `KENN_FORGE_HOOK_GO_CONCURRENCY`
  opts into a cap (`scripts/run-hook-go.sh`, `prek.toml`).
- `internal/server` black-box tests live in sibling test-only packages
  (`accesstest`, `pulltest`, ...), so they build and lint in parallel instead of
  inside the package's single test unit. Put new tests that need only the
  exported API there; whole-server tests go in the `*servertest` packages.
  Tests keep the dependencies they pass to `server.New` (for example the
  syncer) instead of reading them back from `Server` fields. Shared helpers
  live once: root-independent mocks and seeders in `internal/testutil/serverfake`
  (root tests import it too), server-building fixtures in
  `internal/testutil/servertest`; never copy helpers into each test package. `tools/movetests`
  re-runs the split (`tools/movetests/server.json`, `tools/movetests/server-integration.json`).
- CI bounds Go package/test fan-out with `-p` and `-parallel`; do not cap
  `GOMAXPROCS` globally, because test-launched servers inherit that CPU limit.
- Do not overlap frontend/e2e asset builds with Go compilation; replacing embedded
  assets mid-compile causes missing-file build failures (`internal/web/embed.go:9`).
- Frontend API client and schema-constraint generation have one generator shared
  by the Vite plugin and `make api-generate`; hooks must not run a full `vp build`
  to trigger it (`frontend/scripts/generate-api-client.mjs::generateClient`).
- `make api-generate` skips a client generator only when its inputs and its
  generated output both match the last successful run; list every generator
  input and output path (`scripts/cached-generate.sh`). The hook runs in
  parallel with golangci-lint by maintainer decision; a rare one-off collision
  is accepted for the time saved (`prek.toml`).
- Go static-analysis targets run with `-trimpath` so fresh worktrees reuse cached
  export data; tests keep real paths for `runtime.Caller` fixtures
  (`Makefile::GO_ANALYSIS_ENV`). Run standalone analyzers such as NilAway as
  `go vet -vettool` so results are cached per package (`Makefile::nilaway`).
- Reduce scanner pressure at source, not by redirecting `GOTMPDIR`.
- Repository-wide Go tests do not run from Git hooks. Any future fast hook
  lane must select a small set of packages rather than require per-test opt-outs.
- `-short` must skip tests that build and launch real kenn-forge daemons or run
  load-sensitive sync E2Es; those flows belong in full Go lanes (`cmd/kenn-forge/lock_e2e_test.go::buildForgeWithLDFlags`, `internal/server/e2etest/sync_cooldown_test.go::TestTriggerSyncE2EPrioritizesNonDefaultHostFilter`).
- Race tests must synchronize at the narrow boundary under test; do not put a short
  wall-clock deadline around an entire sync pipeline (`internal/github/sync_test.go::TestResolveDisplayNameDedupsConcurrentLookups`).
- Filesystem writes are not a deterministic event burst under load. Test debounce timing
  with controlled event channels and `synctest`; keep real filesystem tests for delivery
  (`internal/configwatch/watcher_test.go::TestWatcher_DebouncesBurst`).
- Build raw-filename fixtures in Git objects without checking them out; host filesystems
  can reject path bytes that Git preserves (`landedwork/range_test.go::TestRebaseFileChanges`).
- Publish fixture output atomically when file existence signals readiness; creation precedes
  completed writes (`internal/server/workspaceapi/agent_resume_test.go::TestRestoreRuntimeSessionsResumesSavedConversationAfterTmuxLoss`).
- Pre-commit runs frontend core checks without full-project Effect diagnostics;
  explicit frontend checks and CI retain Effect coverage (`Makefile::frontend-check-no-deps`).
- `svelte-check` runs with `--tsgo`, which only sees Svelte types that ship
  declarations; source-consumed Svelte dependencies must publish `.d.ts` files
  (`vite.config.ts::svelte-check`).
- Hook `files` patterns mirror what each checker reads (e.g. huma-route-check
  skips test files); do not use `always_run` for whole-module checks (`prek.toml`).
- Package-local `svelte-check` tasks must pass that package's Vite config explicitly;
  implicit discovery can load the root non-Svelte task config (`vite.config.ts:56`).
- Do not use `-v` unless the user requests it or a particular failure genuinely
  needs verbose output.
- Prefer table-driven Go tests. Use testify `require` for preconditions and
  `assert` for non-blocking checks; do not use `t.Fatal`, `t.Fatalf`, `t.Error`,
  `t.Errorf`, `t.Fail`, or `t.FailNow`.
- Import `github.com/stretchr/testify/assert` without an alias. When a test has
  more than three assertions, create `assert := assert.New(t)` and use the
  helper methods thereafter. Kit `testifyhelper` enforces this.
- Prefer the generated Go API client for integration-style API tests.
- Verify generated-client migrations without `-short`; shared workspace fixtures skip
  error-path coverage in short mode (`internal/server/workspacetest/fixtures_test.go::setupWorkspaceServerFixtureWithTmuxInjection`).
- Stage API, generated-client, and module changes before `make huma-check`; its
  Git-index snapshot can otherwise load mismatched types or report old findings
  (`Makefile::huma-check`).
- Use established package fixtures instead of opening databases directly. Use
  `t.TempDir()` when a test needs filesystem isolation.
- Fixed historical timestamps must use an explicit query window or controlled
  clock; rolling defaults otherwise make fixtures expire with wall time
  (`internal/server/e2etest/notifications_test.go::TestNotificationSyncReconcilesReusedRouteE2E`).
- Shell-script tests must execute the script against controlled inputs and
  assert observable output, side effects, or exit status. Do not grep scripts,
  workflows, config, or docs for expected implementation text.
- JavaScript tests that create or mutate Git repositories must use the shared
  isolated fixture; raw child processes can inherit hook bindings and mutate the
  hosting checkout (`scripts/test-git-fixture.mjs::createGitTestRepository`).
- Isolation must strip inherited Git variables, use scratch global and system
  configuration, and keep the fixture outside every checkout; a working
  directory or `git -C` alone does not isolate Git.
- Private tmux tests retain markers on gate, read, validation, or identity errors;
  one deadline covers gate closure and startup draining, and cleanup never addresses
  the default server. (`internal/testutil/testtmux/owner.go::Owner`)
- Tmux recovery reaps only definitely absent identities and preserves state when
  lookup is indeterminate; real tmux ownership is supported only on Darwin and
  Linux, which provide high-resolution process starts. (`internal/testutil/testtmux/process_identity.go::processIdentityStatus`)
- Orphan recovery accepts explicit or exact server-title socket paths only when
  contained by the dedicated root. (`internal/testutil/testtmux/owner.go::reapStaleProcesses`)
- Detached `internal/server` PTY-owner test helpers receive their launching test
  PID explicitly and cancel on Unix reparenting; production PTY owners remain
  durable across daemon restarts. (`internal/server/api_test.go::ptyOwnerHelperExeArgs`)
- Use provider live or container fixtures only when fake transports cannot
  catch endpoint or authentication drift. GitHub GraphQL validation is gated by
  `KENN_FORGE_LIVE_GITHUB_TESTS=1`.
- A claim about how an external CLI behaves must be tested against that real
  CLI with isolated config, skipping when it is absent; a fake written to
  mimic the CLI only proves the fake. Do not test stdlib behavior such as
  environment filtering on its own
  (`internal/config/provider_cli_tokens_test.go::TestGitLabCLITokenForHostIgnoresUnscopedTokenEnvWithRealGlab`).
- The Go test CI job installs those CLIs through mise so the skips do not hide
  the tests; a tool with no build for a maintainer platform belongs in
  `mise.ci.toml` (loaded with `MISE_ENV=ci`), never in `mise.toml`, which must
  stay installable on every workstation. Keep `mise.toml` and `mise.ci.toml` in
  the `ci` path filter, and remember pull requests run `ci.yml@main`, so a
  workflow or tool-install change only takes effect after it merges
  (`.github/workflows/ci-pr.yml`).
- Fixtures asserted through windowed endpoints (activity's default 7d range)
  must seed now-relative instants, not absolute calendar dates — pinned dates
  age out and the test starts failing on a later calendar day
  (`internal/server/e2etest/notifications_test.go::TestNotificationSyncReconcilesReusedRouteE2E`).
- Build boundary-size Git histories through one fast-import stream, and disable
  auto-maintenance on every fixture command; detached commit-graph writes can
  race local clone hardlinks. (`internal/server/repobrowserapi/handler_test.go::serverRepoBrowserGit`)

- Peer transport-failure fixtures must retain their listening address and abort
  the response; a closed listener's port may be reused by another test server
  (`internal/server/e2etest/fleet_snapshot_test.go::TestFleetOperationProxyPeerDispatchFailureE2E`).
