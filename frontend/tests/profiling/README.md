# Workspace-switch profiling

Developer tooling for diagnosing workspace switching latency. One
command drives a real seeded backend (git worktrees + tmux), performs
warm and cold workspace switches against workspaces running an
ordinary shell and an alternate-screen application (`less`), and
captures stable timings plus browser- and Go-side traces for
before/after comparison.

## Running

```bash
make profile-workspace-switch
```

or, from `frontend/`:

```bash
bun run profile:workspace-switch
```

Requires `git`, `tmux`, and `less` on the host (the same requirements
as the real-workspace e2e specs). Knobs, all optional:

| Env var                        | Effect                                                                            |
| ------------------------------ | --------------------------------------------------------------------------------- |
| `MIDDLEMAN_PROFILE_ITERATIONS` | Warm-switch iterations per scenario (default 3)                                   |
| `MIDDLEMAN_PROFILE_OUT_DIR`    | Artifact directory (default `test-results/workspace-switch-profile/<timestamp>/`) |
| `MIDDLEMAN_PROFILE_GO_TRACE=0` | Skip the Go execution trace capture                                               |

## Timing names

The frontend emits User Timing entries via
`src/lib/instrumentation/workspaceSwitchTiming.ts`. The instrumentation
is always on: it records at most nine `performance.measure` calls per
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

| Phase                              | Meaning                                      |
| ---------------------------------- | -------------------------------------------- |
| `workspace-request-start` / `-end` | Workspace metadata API request               |
| `runtime-request-start` / `-end`   | Runtime state (sessions) API request         |
| `fonts-ready`                      | The pane's font-readiness wait resolved      |
| `terminal-constructed`             | Terminal constructed and attached to the DOM |
| `socket-open`                      | Terminal WebSocket reported open             |
| `first-bytes`                      | First binary frame (start of tmux replay)    |
| `first-paint`                      | Frame showing that payload has painted       |

`first-paint` is recorded on the second animation frame after the
first payload finished parsing, i.e. after the frame that renders it
has been presented. Terminal phases are one-shot across all panes of a
switch — the first pane to reach a phase wins, and each measure's
`detail.paneId` says which pane that was — except that `first-paint`
is always recorded by the same pane as `first-bytes`, so
`firstBytesToFirstPaint` describes one real terminal.

Both terminal renderers (xterm and ghostty-web) emit the same names,
but the profiling harness exercises only the default xterm renderer;
treat ghostty numbers from live sessions as informative, not
harness-verified. Derived values in the output answer the usual
questions directly: time before terminal creation
(`routeToTerminalConstructed`), before socket connection
(`routeToSocketOpen`), and between first bytes and visible paint
(`firstBytesToFirstPaint`).

The measures are queryable anywhere —
`performance.getEntriesByName("workspace-switch:first-paint")` in the
DevTools console works against any running middleman, not just this
harness. Each entry's `detail` carries the `workspaceId` it was
recorded for, plus `error: true` on request phases that failed.

For a cold load the zero point is still route selection, which happens
after the SPA boots; use the Chromium trace for the full
navigation-start timeline.

## Artifacts

- `summary.txt` — the per-switch table also printed to the console.
- `timings.json` — every scenario/iteration with raw entries, derived
  metrics, `timeOriginEpochMs` for wall-clock alignment, and an
  `environment` block (commit, browser version, platform, renderer)
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

The harness starts the backend with `MIDDLEMAN_PPROF_ADDR=127.0.0.1:0`,
which serves the standard `net/http/pprof` endpoints (the resolved
address is `pprofAddr` in `timings.json`). The real server supports the
same via `middleman serve -pprof-addr 127.0.0.1:6060` or the
`MIDDLEMAN_PPROF_ADDR` env var.

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
   while reproducing switches (e.g. `MIDDLEMAN_PROFILE_ITERATIONS=10`),
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
2. Start middleman with export enabled:
   `OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 middleman serve`.
3. Perform a workspace switch in the browser, then find its trace ID
   from any `workspace-switch:*` measure's `detail.traceId` (e.g.
   `performance.getEntriesByName("workspace-switch:first-paint")[0].detail.traceId`
   in the DevTools console).
4. In Tempo (via Grafana at `http://127.0.0.1:3000`), search for that
   trace ID directly, or by the `workspace.id` span attribute. Expect
   HTTP spans named after the matched route (e.g.
   `GET /workspaces/{id}`) and a bounded `terminal.attach` span for the
   WS attach.
