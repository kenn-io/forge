# Workspace-switch profiling

Developer tooling for diagnosing workspace switching latency. One
command drives a real seeded backend (git worktrees + tmux), performs
retained-hit, forced-miss, and cold workspace switches against workspaces running an
ordinary shell and an alternate-screen application (`less`), and
captures stable timings plus browser- and Go-side traces for
before/after comparison.

## Running

```bash
make profile-workspace-switch
```

or, from `frontend/`:

```bash
node node_modules/.bin/playwright test --config tests/profiling/playwright-profile.config.ts workspace-switch.spec.ts
```

Requires `git`, `tmux`, and `less` on the host (the same requirements
as the real-workspace e2e specs). Knobs, all optional:

| Env var                         | Effect                                                                            |
| ------------------------------- | --------------------------------------------------------------------------------- |
| `KENN_FORGE_PROFILE_ITERATIONS` | Warm-switch iterations per hit/miss scenario (minimum and default 10)             |
| `KENN_FORGE_PROFILE_OUT_DIR`    | Artifact directory (default `test-results/workspace-switch-profile/<timestamp>/`) |
| `KENN_FORGE_PROFILE_GO_TRACE=0` | Skip the Go execution trace capture                                               |

## Timing names

The frontend emits User Timing entries via
`src/lib/instrumentation/workspaceSwitchTiming.ts`. The instrumentation
is always on: it records at most ten `performance.measure` calls per
workspace switch and nothing else, so there is no production telemetry
and no measurable steady-state cost. Repeated work inside one switch
(runtime polling, reconnects, extra panes) does not re-record a phase,
which keeps the numbers stable across runs. Phases arriving more than
30 seconds after route selection are dropped (they belong to a later
user action, not the switch), and leaving the workspace surface
cancels the switch outright.

Every measure is named `workspace-switch:<phase>` and its duration is
the time from route selection (the terminal view reacting to the new
workspace route) to that phase:

| Phase                              | Meaning                                             |
| ---------------------------------- | --------------------------------------------------- |
| `workspace-request-start` / `-end` | Workspace metadata API request                      |
| `runtime-request-start` / `-end`   | Runtime state (sessions) API request                |
| `fonts-ready`                      | The pane's font-readiness wait resolved             |
| `terminal-constructed`             | Terminal constructed and attached to the DOM        |
| `socket-open`                      | Terminal WebSocket reported open                    |
| `first-bytes`                      | First binary frame (start of tmux replay)           |
| `first-paint`                      | Frame showing that payload has painted              |
| `retained-first-paint`             | Second frame after a retained wrapper is reattached |

`first-paint` is recorded on the second animation frame after the
first payload finished parsing, i.e. after the frame that renders it
has been presented. Terminal phases are one-shot across all panes of a
switch — the first pane to reach a phase wins, and each measure's
`detail.paneId` says which pane that was — except that `first-paint`
is always recorded by the same pane as `first-bytes`, so
`firstBytesToFirstPaint` describes one real terminal.

On a retained hit there is deliberately no terminal construction, socket open,
or replay. The pool records `retained-first-paint` after reparenting and
recreating the renderer; the profiler uses it as that scenario's route-to-paint
endpoint while preserving every existing phase name for miss comparisons.

The xterm terminal emits these names. Derived values in the output answer the usual
questions directly: time before terminal creation
(`routeToTerminalConstructed`), before socket connection
(`routeToSocketOpen`), and between first bytes and visible paint
(`firstBytesToFirstPaint`).

The measures are queryable anywhere —
`performance.getEntriesByName("workspace-switch:first-paint")` in the
DevTools console works against any running kenn-forge, not just this
harness. Each entry's `detail` carries the `workspaceId` it was
recorded for, plus `error: true` on request phases that failed.

For a cold load the zero point is still route selection, which happens
after the SPA boots; use the Chromium trace for the full
navigation-start timeline.

## Artifacts

- `summary.txt` — the per-switch table also printed to the console.
- `timings.json` — every scenario/iteration with raw entries, derived
  metrics, `timeOriginEpochMs` for wall-clock alignment, and an
  `environment` block (commit, browser version, platform)
  for comparing runs. Written before the harness's own assertions, so
  a failed run still leaves its evidence. The harness also verifies
  the alternate-screen scenario is real by asking the e2e server's
  tmux for a pane with `alternate_on=1` running the pager.
- `trace.chrome.json` — Chromium trace including the
  `workspace-switch:*` timings, network, and frame events. Open in
  [Perfetto](https://ui.perfetto.dev) or DevTools Performance → Load
  profile.
- `go-trace.out` — Go execution trace from the backend spanning the
  warm-switch window. Open with `go tool trace go-trace.out`.

## Correlating browser timings with Go pprof

The harness starts the backend with `KENN_FORGE_PPROF_ADDR=127.0.0.1:0`,
which serves the standard `net/http/pprof` endpoints (the resolved
address is `pprofAddr` in `timings.json`). The real server supports the
same via `kenn-forge serve -pprof-addr 127.0.0.1:6060` or the
`KENN_FORGE_PPROF_ADDR` env var.

To line up a browser phase with Go-side work:

1. Compute the phase's wall-clock time from `timings.json`:
   `timeOriginEpochMs + entry.startTime + entry.duration` is the epoch
   milliseconds at which the phase completed (`+ startTime` alone is
   route selection).
2. The Go execution trace records a wall-clock sync (visible in
   `go tool trace` and in `go tool trace -d=parsed`), so those epoch
   times identify the matching region of the trace. Look at goroutines
   in the WebSocket attach and tmux paths between route selection and
   `first-bytes`.
3. For sampled CPU profiles instead of traces, capture
   `curl -o cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=10"`
   while reproducing switches (e.g. `KENN_FORGE_PROFILE_ITERATIONS=10`),
   then `go tool pprof cpu.pprof`. Sampling windows longer than 30s are
   rejected by the profiler listener.

Note the profiler rejects requests that look like they come from a
browser without same-origin fetch metadata; `curl` and `go tool pprof`
work as-is, but custom clients must not send a browser User-Agent.

## Live tracing

W3C trace context propagation (frontend-minted `traceparent`/`baggage`
on every API request and terminal WS attach) is always on; exporting
those traces to an OTel backend is opt-in. To inspect a live trace:

1. Start a local all-in-one OTLP collector + Grafana/Tempo UI:
   `make otel-lgtm` (requires Docker; serves the UI at
   `http://127.0.0.1:3000` and OTLP on `4317`/`4318`).
2. Start kenn-forge with export enabled:
   `OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 kenn-forge serve`.
3. Perform a workspace switch in the browser, then find its trace ID
   from any `workspace-switch:*` measure's `detail.traceId` (e.g.
   `performance.getEntriesByName("workspace-switch:first-paint")[0].detail.traceId`
   in the DevTools console).
4. In Tempo (via Grafana at `http://127.0.0.1:3000`), search for that
   trace ID directly, or by the `workspace.id` span attribute. Expect
   HTTP spans named after the matched route (e.g.
   `GET /workspaces/{id}`) and a bounded `terminal.attach` span for the
   WS attach.

## Slow-link navigation benchmark

From the repository root:

```bash
node node_modules/vite-plus/bin/vp run kenn-forge-frontend#profile:slow-link
```

This starts the existing isolated e2e server with synthetic provider fixtures
and temporary Git repositories. It builds the production
frontend when needed. It requires Git; it never connects to a live
Forge daemon. Run it separately from other browser suites and builds.

Chromium emulates **700 ms latency and 1 Mbit/s in each direction**
(`125000` bytes/second), then a **3-second offline window**. These are illustrative
conditions, not a model of a particular satellite service. The throttled link is
browser to daemon; fixture setup and provider work are local and unthrottled.
This benchmark covers HTTP navigation. Packet loss is not injected;
TCP and TLS overhead are not included in the byte counters. For terminal
switching and paint timings, use the existing `make profile-workspace-switch`
harness described above; it does not apply this HTTP network profile.

The benchmark writes `timings.json` and `summary.txt` under
`frontend/test-results/slow-link-profile/<timestamp>/`:

- Cold pull-list visibility from navigation start, in a fresh browser context.
- First and repeated PR/issue navigation until the item's title is visible, with
  three repeat passes over two items of each kind. Each detail HTTP response
  settles before the next visit. Diff and auxiliary-pane readiness are outside
  this measurement.
- Comment POST acknowledgement and visible comment feedback against the fixture
  provider. This does not estimate a real upstream provider's write latency.
- Explicit return navigation after the outage. This measures a user retry,
  rather than claiming automatic recovery of every live connection.
- HTTP encoded response-body bytes and request
  counts over a 60-second idle pull-list window. Header bytes and transport
  overhead are excluded. The fixture server does not run provider background sync.

There are no performance thresholds. Visibility and API success
checks keep broken scenarios from silently producing plausible measurements.
Timings use a small fixed sample, so compare repeated runs before attributing a
small difference to code. Artifacts are written even when a scenario fails.

Optional environment variables:

| Variable                       | Purpose                                                                                                                  |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------ |
| `KENN_FORGE_PROFILE_OUT_DIR`   | Explicit artifact directory, including a path outside the worktree.                                                      |
| `KENN_FORGE_PROFILE_LABEL`     | Build label recorded with browser and platform versions; defaults to Git HEAD.                                           |
| `KENN_FORGE_PROFILE_BASELINE`  | Earlier `timings.json`; adds per-scenario median deltas to the summary.                                                  |
| `PLAYWRIGHT_E2E_SERVER_BINARY` | Existing isolated e2e binary with its own embedded production bundle, useful for an unchanged baseline after rebuilding. |

For comparisons, preserve the baseline e2e binary before rebuilding, run each
binary with the same Chromium version, and set `KENN_FORGE_PROFILE_BASELINE` on
the second run. A negative timing delta means the second run was faster.
