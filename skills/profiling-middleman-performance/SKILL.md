---
name: profiling-middleman-performance
description: Use when diagnosing Middleman workspace-switch, terminal-rendering, browser-main-thread, API latency, allocation, CPU, tmux replay, or frontend/backend trace regressions with pprof, Playwright, User Timing, Go trace, or OpenTelemetry.
---

# Profiling Middleman Performance

## Core Rule

Measure the user-visible phase first, then use backend profiles to explain it. Keep the before/after workload, data, renderer, iteration count, and tracing configuration identical.

**REQUIRED SUB-SKILL:** Use middleman-ephemeral-dev for copied-state stacks.

## Choose the Lane

| Question | Primary tool |
| --- | --- |
| Why is workspace switching or first paint slow? | `make profile-workspace-switch` |
| Why is a real-data API slow or allocation-heavy? | copied-state `dev-ephemeral` plus pprof and repeated `curl` |
| Which browser request matches which Go operation? | User Timing wall-clock correlation with Go trace |
| Which distributed span owns the delay? | opt-in OTel/Tempo diagnostic run after primary timing |

Read [references/workflows.md](references/workflows.md) for exact commands and artifacts.

## Workflow

1. Record provenance before measuring: commit SHA, dirty diff state, browser, renderer, platform, and command. `timings.json` identifies `HEAD`, not uncommitted changes; commit first or preserve the tested patch explicitly.
2. Capture a baseline before changing code. Use at least ten warm iterations; treat the harness's single cold sample as descriptive unless running multiple independent captures.
3. Locate the phase:
   - request end slow: API/database/backend;
   - construction slow: Svelte/xterm/main thread;
   - socket-open to first-bytes slow: WebSocket/tmux attach or replay;
   - first-bytes to first-paint slow: terminal parsing/rendering/paint.
4. Change one hypothesis at a time. Rerun the same lane and preserve raw artifacts, including outliers.
5. For pprof deltas, separate request-path nodes from unrelated background observers. If background work dominates, report the process delta only as an upper bound; do not divide it by request count and label it request allocation.
   A per-request allocation claim requires an isolated handler/benchmark lane with background observers absent.
6. Reject changes whose measured benefit is smaller than run-to-run variance or disproportionate to memory, GPU, correctness, or maintenance cost.
7. Report sample count, median, p95 convention, absolute delta, percentage delta, correctness checks, and artifact paths.

## Evidence Contract

Keep:

- `summary.txt`, `timings.json`, `trace.chrome.json`, and `go-trace.out` for switch runs;
- before/after pprof files and `go tool pprof -top` output for backend runs;
- an adjacent equal-duration idle-control profile when background work is material;
- the copied status JSON and exact pprof address;
- a table separating ordinary-shell and alternate-screen results;
- accepted improvements and audited no-change decisions.

## Common Mistakes

- Benchmarking a dirty tree while reporting only `HEAD`.
- Using three iterations or presenting one cold sample as a distribution.
- Comparing different databases, browser versions, renderers, or trace settings.
- Discarding outliers without trace evidence.
- Treating `first-paint` as LCP; it is Middleman's second-animation-frame terminal marker.
- Profiling through a browser request; the pprof listener expects loopback tooling such as `curl`.
- Stopping a stack or tmux session not created for the profiling run.
