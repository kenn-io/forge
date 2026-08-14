# Test Basics

Use this document when writing or running Go tests, choosing common test
fixtures, or changing shell-script coverage.

- Pass `-shuffle=on` when invoking `go test` directly; `make test` and
  `make test-short` already include it. Do not pass redundant `-count=1`; use
  `-count=N` only when `N > 1` for repeated runs.
- Local Go hooks must use bounded concurrency and separate heavy consumers into
  ordered priority groups; keep the shared Go cache across worktrees (`scripts/run-hook-go.sh`, `prek.toml:53`).
- `-short` must skip tests that build and launch real kenn-forge daemons or run
  load-sensitive sync E2Es; those flows belong in full Go lanes (`cmd/kenn-forge/lock_e2e_test.go::buildForgeWithLDFlags`, `internal/server/e2etest/sync_cooldown_test.go::TestTriggerSyncE2EPrioritizesNonDefaultHostFilter`).
- Pre-commit runs frontend core checks without full-project Effect diagnostics;
  explicit frontend checks and CI retain Effect coverage (`Makefile::frontend-check-no-deps`).
- Do not use `-v` unless the user requests it or a particular failure genuinely
  needs verbose output.
- Prefer table-driven Go tests. Use testify `require` for preconditions and
  `assert` for non-blocking checks; do not use `t.Fatal`, `t.Fatalf`, `t.Error`,
  `t.Errorf`, `t.Fail`, or `t.FailNow`.
- Import `github.com/stretchr/testify/assert` without an alias. When a test has
  more than three assertions, create `assert := assert.New(t)` and use the
  helper methods thereafter.
- Prefer the generated Go API client for integration-style API tests.
- Use established package fixtures instead of opening databases directly. Use
  `t.TempDir()` when a test needs filesystem isolation.
- Fixed historical timestamps must use an explicit query window or controlled
  clock; rolling defaults otherwise make fixtures expire with wall time
  (`internal/server/e2etest/notifications_test.go::TestNotificationSyncReconcilesReusedRouteE2E`).
- Shell-script tests must execute the script against controlled inputs and
  assert observable output, side effects, or exit status. Do not grep scripts,
  workflows, config, or docs for expected implementation text.
- Private tmux tests retain markers on gate, read, validation, or identity errors;
  one deadline covers gate closure and startup draining, and cleanup never addresses
  the default server. (`internal/testutil/testtmux/owner.go::Owner`)
- Tmux recovery reaps only definitely absent identities and preserves state when
  lookup is indeterminate; real tmux ownership is supported only on Darwin and
  Linux, which provide high-resolution process starts. (`internal/testutil/testtmux/process_identity.go::processIdentityStatus`)
- Orphan recovery accepts explicit or exact server-title socket paths only when
  contained by the dedicated root. (`internal/testutil/testtmux/owner.go::reapStaleProcesses`)
- Use provider live or container fixtures only when fake transports cannot
  catch endpoint or authentication drift. GitHub GraphQL validation is gated by
  `KENN_FORGE_LIVE_GITHUB_TESTS=1`.
- Fixtures asserted through windowed endpoints (activity's default 7d range)
  must seed now-relative instants, not absolute calendar dates — pinned dates
  age out and the test starts failing on a later calendar day
  (`internal/server/e2etest/notifications_test.go::TestNotificationSyncReconcilesReusedRouteE2E`).
- Build boundary-size Git histories through one fast-import stream, not a
  subprocess per add/commit; parallel packages share process capacity
  (`internal/testutil/gitfixture/history.go::AppendFileCommits`).
