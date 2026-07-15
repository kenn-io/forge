# Middleman Performance Workflows

## Workspace-switch harness

```sh
MIDDLEMAN_PROFILE_ITERATIONS=10 \
MIDDLEMAN_PROFILE_OUT_DIR=frontend/tmp/perf-before \
make profile-workspace-switch
```

Repeat after one isolated change with a new output directory. Compare ordinary-shell and alternate-screen scenarios separately. The phase definitions and trace-correlation formula live in `frontend/tests/profiling/README.md`.

Primary fields from `timings.json`:

- workspace/runtime request end;
- fonts ready and terminal constructed;
- socket open and first bytes;
- first paint and first-bytes-to-first-paint;
- `timeOriginEpochMs + startTime + duration` for Go-trace wall-clock alignment.

## Copied-state API and pprof

Choose a free loopback profiler port and a unique work directory:

```sh
MIDDLEMAN_PPROF_ADDR=127.0.0.1:6060 \
make dev-ephemeral ARGS="-work-dir tmp/perf-copied"
```

Read `tmp/perf-copied/dev-ephemeral.json` for backend/frontend URLs and PIDs. Record the separately chosen pprof address.

Warm latency sample:

```sh
BACKEND=$(jq -r .backend_url tmp/perf-copied/dev-ephemeral.json)
for i in $(seq 1 30); do
  curl -fsS -o /dev/null -w '%{time_total}\n' "$BACKEND/api/v1/workspaces"
done
```

Allocation delta around the same request count:

```sh
curl -fsS -o before.pprof 'http://127.0.0.1:6060/debug/pprof/heap?gc=1'
for i in $(seq 1 50); do
  curl -fsS -o /dev/null "$BACKEND/api/v1/workspaces"
done
curl -fsS -o after.pprof 'http://127.0.0.1:6060/debug/pprof/heap?gc=1'
go tool pprof -top -alloc_space -base before.pprof after.pprof
```

Capture an adjacent equal-duration idle delta when observers or git subprocesses appear. Live-process deltas explain call sites and bound process allocation; use an isolated Go benchmark with background loops disabled before claiming allocations per request.

Capture CPU during an active reproduction window:

```sh
curl -fsS -o cpu.pprof \
  'http://127.0.0.1:6060/debug/pprof/profile?seconds=10'
go tool pprof -top -cum cpu.pprof
```

The profiler caps CPU/trace windows at 30 seconds. Use `curl` or `go tool pprof`, not browser fetches.

## Cleanup

Stop only the stack created for the run:

```sh
make dev-ephemeral-stop ARGS="-work-dir tmp/perf-copied"
```

Before stopping any other resource, follow the repository's process-cleanup approval rule.
