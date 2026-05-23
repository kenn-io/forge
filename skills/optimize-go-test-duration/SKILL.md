---
name: optimize-go-test-duration
description: Use when Go test wall time is too high, CI test duration regresses, a package needs trace-driven test performance analysis, or tests should be made safely parallel with t.Parallel, package splitting, subprocess isolation, semaphores, or Go built-in tracing.
---

# Optimize Go Test Duration

## Core Idea

Optimize wall time from evidence, not guesses. Separate actual slow work from time spent waiting behind the Go test scheduler, package boundaries, subprocess startup, locks, I/O, sleeps, or scarce external resources.

Keep coverage and realism unless the user explicitly asks to trade them away.

## Baseline

Use comparable commands before and after each change.

For package timing:

```bash
go test ./path/to/pkg/... -shuffle=on
```

For per-test timing:

```bash
go test ./path/to/pkg -shuffle=on -json > /tmp/pkg-tests.json
jq -r 'select(.Action=="pass" and .Test != null and .Elapsed != null) | [.Elapsed, .Test] | @tsv' /tmp/pkg-tests.json | sort -nr | head -60
```

Follow repository test rules first. In middleman, always pass `-shuffle=on` for direct `go test`, do not add `-count=1`, and do not use `-v` unless needed for a specific failure.

## Trace Workflow

Capture a Go trace for the slow package or focused group:

```bash
go test ./path/to/pkg -shuffle=on -trace /tmp/pkg.trace
```

Extract profiles that explain where wall time went:

```bash
go tool trace -pprof=sched /tmp/pkg.trace > /tmp/pkg-sched.pprof
go tool trace -pprof=sync /tmp/pkg.trace > /tmp/pkg-sync.pprof
go tool trace -pprof=syscall /tmp/pkg.trace > /tmp/pkg-syscall.pprof
go tool trace -pprof=net /tmp/pkg.trace > /tmp/pkg-net.pprof
go tool pprof -top /tmp/pkg-sched.pprof
go tool pprof -top /tmp/pkg-sync.pprof
go tool pprof -top /tmp/pkg-syscall.pprof
go tool pprof -top /tmp/pkg-net.pprof
```

Read the results this way:

- Large time in `testing.(*T).Parallel`, `testing.(*testState).waitParallel`, or scheduler delay means tests are queued behind serial work or the package parallelism limit.
- Large `os/exec`, `os.StartProcess`, or `os.Process.Wait` time points at subprocess setup, helper processes, shell wrappers, or cleanup.
- Large `sync.Mutex`, channel receive, or condition wait time points at shared fixtures, global locks, single-flight code, or test helpers that serialize work.
- Large syscall or net time points at real I/O, sockets, PTY/tmux sessions, filesystem watchers, or cleanup.

If the traced run fails but still writes a trace, inspect it anyway, then reproduce the failure with a focused command before editing.

## Safe Parallelization

Prefer `t.Parallel()` for isolated tests whose fixtures are independent. Call it near the top, after skips and before expensive setup.

Do not use parent-process global mutation in parallel tests:

- Avoid `t.Setenv` in tests that should run parallel; it changes the current test process environment and prevents safe parallelism.
- Pass per-child environment with `exec.Cmd.Env` when only the child needs it.
- Prefer argv markers for Go helper subprocesses, for example `os.Args[0] -test.run=TestHelper -- helper-marker mode`.
- Avoid `os.Chdir`, global temp paths, package-level mutable state, shared ports, and shared database files in parallel tests.

Use `t.TempDir()`, isolated SQLite files, loopback listeners on random ports, unique workspace names, and per-test command arguments.

## Scarce Resources

Real PTY, tmux, browser, container, or live-service tests can still run in parallel, but bound resource pressure.

Use a package-level weighted semaphore:

```go
var ptySem = semaphore.NewWeighted(4)

func runParallelPTYE2E(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.NoError(t, ptySem.Acquire(t.Context(), 1))
	t.Cleanup(func() { ptySem.Release(1) })
}
```

Pick the limit from observed stability and machine constraints. Start conservative, then raise only when repeated runs stay reliable.

## Optimization Moves

Use the trace to choose the smallest useful move:

- Add `t.Parallel()` to isolated tests that are only waiting behind the test runner.
- Remove `t.Setenv` or other parent-process global mutations by moving state into fixture structs, command args, or child `Env`.
- Split large packages when independent test groups are blocked by unrelated serial tests and package boundaries are natural.
- Replace fixed sleeps with readiness probes, channels, eventual assertions, or explicit synchronization.
- Share expensive immutable setup only when it cannot leak mutable state between tests.
- Keep full-stack/e2e coverage when the behavior needs real HTTP, SQLite, PTY, tmux, or subprocesses; make the realism concurrent instead of deleting it.

## Verification

After each meaningful change:

1. Run the focused affected tests with `-shuffle=on`.
2. Run the package or subtree baseline command again.
3. Compare wall time, not just individual test elapsed time; parallel tests may report elapsed time that includes queueing.
4. Re-run or trace again if the result is noisy or the bottleneck moved.
5. Commit only after the relevant checks pass.

Report the before/after numbers in seconds and percent reduction:

```text
before: 170s
after: 66s
improvement: 104s faster, 61% lower wall time, 2.6x faster
```
