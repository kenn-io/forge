# Test Basics

Use this document when writing or running Go tests, choosing common test
fixtures, or changing shell-script coverage.

## Running tests and checks

- Pass `-shuffle=on` when invoking `go test` directly; `make test` and
  `make test-short` already include it. Do not pass redundant `-count=1`; use
  `-count=N` only when `N > 1` for repeated runs.
- Do not use `-v` unless the user requests it or a failure genuinely needs it.
- Before pushing Go changes, run `make nilaway`, `make fmt-check`, and
  `make lint-check`; `golangci-lint run` does not apply v2 formatters, so formatter
  drift is a separate gate (`Makefile::fmt-check`).
- Direct Make test lanes bound package concurrency (`GO_TEST_P=` restores native);
  Go hooks run uncapped unless `KENN_FORGE_HOOK_GO_CONCURRENCY` is set (`scripts/run-hook-go.sh`).
- CI bounds Go fan-out with `-p` and `-parallel`; do not cap `GOMAXPROCS` globally,
  because test-launched servers inherit that CPU limit.
- `-short` must skip tests that build and launch real kenn-forge daemons or run
  load-sensitive sync E2Es (`cmd/kenn-forge/lock_e2e_test.go::buildForgeWithLDFlags`).
- Verify generated-client migrations without `-short`; shared workspace fixtures skip
  error-path coverage in short mode.
- Stage API, generated-client, and module changes before `make huma-check`; it reads
  a Git-index snapshot (`Makefile::huma-check`).
- Do not overlap frontend/e2e asset builds with Go compilation; replacing embedded
  assets mid-compile breaks the build (`internal/web/embed.go:9`).
- Mock Playwright tests use built assets; the development module graph makes
  concurrent navigation compete with module loading (`frontend/playwright.config.ts`).
- Reduce scanner pressure at source, not by redirecting `GOTMPDIR`.

## Hooks and generators

- Commit hooks run only formatters, generators, and checks scoped to the change or a
  few seconds long; whole-repo lint, type checks, contract checks, and script tests
  run at pre-push (`prek.toml`). CI's `make fmt-check` covers the whole repo.
- Repository-wide Go tests do not run from Git hooks; a future fast hook lane must
  select a small package set rather than require per-test opt-outs.
- Hook `files` patterns mirror what each checker reads; do not use `always_run` for
  whole-module checks. API generation runs for every non-test Go change because
  schema types cross package boundaries (`prek.toml`).
- Frontend API client and schema-constraint generation share one generator; hooks
  must not run a full `vp build` to trigger it (`frontend/scripts/generate-api-client.mjs`).
- `make api-generate` skips a generator only when its inputs and output match the
  last run; list every input and output path (`scripts/cached-generate.sh`).
- Run standalone analyzers such as NilAway as `go vet -vettool` so results cache per
  package (`Makefile::nilaway`).
- Pre-push frontend core checks skip full-project Effect diagnostics; explicit
  frontend checks and CI keep them (`Makefile::frontend-check-no-deps`).
- `svelte-check` runs with `--tsgo`, which sees only Svelte types that ship
  declarations; source-consumed Svelte dependencies must publish `.d.ts` files.
- Package-local `svelte-check` tasks pass that package's Vite config explicitly;
  implicit discovery can load the root config (`vite.config.ts:56`).
- The Go test CI job installs real external CLIs through mise. Agent CLIs (`codex`,
  `codex-acp`) are CI-only too: agents launched in a checkout resolve their binary through
  `mise.toml`, so a pin there downgrades the user's agent. A tool with no build for a maintainer
  platform goes in `mise.ci.toml`, never `mise.toml`, which must install on every workstation.
  Keep both files in the `ci` path filter.
- PRs run `ci.yml@main`, so workflow or tool-install changes take effect only after
  merge; exercise them first with `gh workflow run ci.yml --ref <branch>`.

## Writing Go tests

- Prefer table-driven tests. Use testify `require` for preconditions and `assert`
  for non-blocking checks; never use `t.Fatal`, `t.Fatalf`, `t.Error`, `t.Errorf`,
  `t.Fail`, or `t.FailNow`.
- Import testify `assert` without an alias; with more than three assertions use
  `assert := assert.New(t)` (kit `testifyhelper` enforces this).
- `internal/server` black-box tests live in sibling test-only packages so they build
  and lint in parallel; whole-server tests go in `*servertest` packages.
- Tests keep the dependencies they pass to `server.New` instead of reading them back
  from `Server` fields.
- Shared server test helpers live once (`internal/testutil/serverfake`,
  `internal/testutil/servertest`); never copy helpers into each test package.
- Use established package fixtures instead of opening databases directly; use
  `t.TempDir()` for filesystem isolation. Unix-socket fixtures keep a short `/tmp` root.
- `t.Context()` is already canceled inside `t.Cleanup`; shutdown helpers use
  `context.WithoutCancel(t.Context())` or `context.Background()`.
- In test-owned timeout failures, call `stacktest.Dump(t)` before cleanup releases
  blocked work so logs keep its stacks.
- Clone fetches outlive their workspace setup and server shutdown; fixtures owning a
  `gitclone.Manager` register `t.Cleanup(clones.Wait)`.
- Huma ignores unexported embedded input structs for path parameters; keep path
  fields explicit (`internal/server/pullapi/update_branch.go::updateBranchInput`).
- Never locate files from `runtime.Caller` or stack-trace paths; builds are trimmed.
  Tests run from their package directory (`Makefile::GOFLAGS`).
- Trimmed test binaries cannot fall back to Go's bundled zoneinfo; packages loading
  legacy zone names, or any named zone on Windows, import `time/tzdata` from a `_test.go` file.
- Race tests synchronize at the narrow boundary under test; never put a short
  wall-clock deadline around an entire sync pipeline.
- Eventual-poll callbacks can outlive their test; return errors to the test goroutine
  or use `CollectT` so late failures cannot panic the package.
- Real CLI confirmation prompts may render before their input guard settles; wait for
  a stable selection before submitting.
- Filesystem writes are not a deterministic event burst under load; test debounce
  timing with controlled channels and `synctest`, and real filesystems only for delivery.
- Publish fixture output atomically when file existence signals readiness.
- Fixtures with fixed historical timestamps need an explicit query window or
  controlled clock; fixtures read through windowed endpoints (activity's 7d default)
  seed now-relative instants (`internal/server/e2etest/notifications_test.go`).
- Config path assertions expect physical directory identity; macOS temp parents can
  be symlink aliases. Compare fixture working directories by file identity; Windows
  can use short-name paths.
- Base terminal tests inherit the user's login shell; run POSIX expansions through an
  explicit `/bin/sh -c`.
- Peer transport-failure fixtures keep their listening address and abort the
  response; a closed listener's port may be reused by another test server.
- Detached daemon fixtures wait for process termination before removing binaries;
  Windows `Process.Kill` only requests it, and cleanup retries on any Windows error
  since sharing violations do not match `os.ErrPermission`.

## External tools and CLIs

- Shell-script tests execute the script against controlled inputs and assert output,
  side effects, or exit status; never grep scripts, workflows, config, or docs for
  implementation text.
- A claim about an external CLI's behavior is tested against the real CLI with
  isolated config, skipping when absent; a mimicking fake only proves the fake. Do
  not test stdlib behavior on its own.

## Git fixtures

- Go tests run Git only through `internal/testutil/gitfixture` or the `gitsafe`
  runners, and each such package calls `gitsafe.RunIsolatedMain` from `TestMain`
  (`make git-test-fixture-check`, `tools/norawgittest/main.go`).
- Use `Runner` where code strips Git variables and `MutableRunner` only for global
  config mutations; `gitcmd.New()` is not a substitute, since it probes user
  `safe.directory` settings.
- Use `gitfixture.ScratchDir(t)` before initializing custom Git fixtures; `t.TempDir()`
  can land inside a checkout when `TMPDIR` points there.
- Isolation must strip inherited Git variables, use scratch global and system config,
  and keep the fixture outside every checkout; a working directory or `git -C` alone
  does not isolate Git.
- JavaScript tests that create or mutate repositories use the shared isolated fixture
  (`scripts/test-git-fixture.mjs::createGitTestRepository`); raw child processes can
  inherit hook bindings and mutate the hosting checkout.
- Auto-GC and auto-maintenance stay off in synthetic repositories; detached
  maintenance races cleanup and local-clone hardlinks. A push into a local bare repo
  runs receive-pack without the pusher's `-c` options (`internal/testutil/gitsafe/gitsafe.go::SharedConfig`).
- Build boundary-size histories through one fast-import stream.
- Build raw-filename fixtures in Git objects without checking them out; host
  filesystems can reject path bytes Git preserves.
- Git 2.56+ updates a configured remote's tracking refs, including fetched pull refs,
  on a push to that remote's URL; a fork-push fixture must push to a repository no
  configured remote resolves to.

## tmux, PTY, and ACP fixtures

- Keystrokes sent to a freshly attached terminal can be lost while the tmux client
  starts; resend until the output arrives.
- ACP fixtures explicitly select private tmux or disable tmux discovery; a nil command
  discovers the live default server.
- Tests that set `localruntime.Options.TmuxCommand` use `NewTmuxRuntimeFixture`.
- ACP owner processes can outlive manager stop; await their exit before deleting
  fixture state. Saved-chat integration checks use the owner-process fixture.
- Private tmux tests keep markers on gate, read, validation, or identity errors, and
  cleanup never addresses the default server (`internal/testutil/testtmux/owner.go::Owner`).
- Tmux recovery reaps only definitely absent identities and preserves state when
  lookup is indeterminate; real tmux ownership is supported only on Darwin and Linux.
- Orphan recovery accepts socket paths only when contained by the dedicated root.
- Detached `internal/server` PTY-owner test helpers cancel on Unix reparenting from
  their launching test; production PTY owners stay durable across daemon restarts.

## Build cache and debugging

Make and CI build with `-trimpath` so every worktree shares one Go build cache
(`Makefile::GOFLAGS`). `GOFLAGS=-trimpath=false` opts out; plain `go` commands
get it after `go env -w GOFLAGS=-trimpath`.

- `dlv debug` and `dlv test` lose source lookup; pass `--build-flags=-trimpath=false`.
- For `dlv exec` or `dlv attach` on a trimmed binary, start Delve at the
  repository root and run `config substitute-path -guess` (Delve 1.24+).
