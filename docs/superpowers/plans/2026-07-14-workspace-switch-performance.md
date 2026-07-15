# Workspace-Switch Performance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce workspace-switch latency and eliminate unrelated foreground work while preserving a reproducible telemetry trail for every accepted or rejected optimization.

**Architecture:** Use PR #674's `workspace-switch:*` User Timing measures as the browser-side source of truth and correlate them with the existing Go trace, pprof, and opt-in OTel spans. Apply isolated changes to the workspace view, terminal renderer, terminal attach path, and provider refresh path; rerun the same benchmark after each change so the final report can attribute uplift rather than only compare the endpoints.

**Tech Stack:** Svelte 5, TypeScript, xterm.js, Vite+, Playwright, Go, Huma, tmux, OpenTelemetry, Go pprof/trace, Kata.

## Global Constraints

- Keep the `workspace-switch:*` timing names introduced by PR #674 stable.
- Use `bun` and Vite+ tooling; never use npm.
- Write a failing behavioral test before each production change.
- Run direct Go tests with `-shuffle=on` and without `-count=1` or `-v`.
- Do not add production telemetry export; tracing remains opt-in.
- Do not add compatibility shims or dual behavior paths.
- Each accepted optimization gets an isolated commit and before/after measurement.
- Reject an optimization when telemetry shows negligible benefit relative to its memory, GPU, correctness, or maintenance cost; record that evidence in Kata.

## Baseline

Commit `772ff3009b84cff43dd3b24f5c84c663f8e53e55`, Chromium `149.0.7827.55`, macOS arm64, xterm renderer, ten warm iterations:

- Warm ordinary-shell first paint: approximately 171-186 ms, with one 180-186 ms cluster and route-to-first-bytes around 47-61 ms.
- Warm alternate-screen first paint: approximately 168-199 ms, route-to-first-bytes around 46-72 ms.
- Runtime request starts only after workspace metadata completes; the serialized workspace phase costs roughly 20-45 ms warm.
- First bytes to first paint is approximately 121-132 ms because the metric intentionally waits for two animation frames.
- Real copied state: `/api/v1/workspaces` is approximately 48-104 ms for nine workspaces and returns 14.7 KB.
- Real copied state: `/api/v1/activity` is approximately 126-136 ms, returns 7.32 MB, and contains the 5,000-item cap.

Artifacts live under ignored path `frontend/tmp/perf-workspace-baseline/`.

---

### Task 1: Start workspace metadata and runtime requests concurrently (`bd9s`)

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Test: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`
- Measure: `frontend/tests/profiling/workspace-switch.spec.ts`

**Interfaces:**
- Consumes: `fetchWorkspace(): Promise<void>` and `fetchRuntime(): Promise<WorkspaceRuntimeState | null>`.
- Produces: route initialization where both requests are started in the same synchronous turn and existing in-flight dedup prevents the ready-workspace callback from duplicating runtime work.

- [ ] Add a component test using deferred workspace and runtime promises. Render a ready workspace route and assert `getWorkspaceRuntime` is called before the deferred workspace response resolves.
- [ ] Run `node node_modules/vite-plus/bin/vp test frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts` and confirm the new assertion fails because runtime starts after workspace completion.
- [ ] Change route initialization to save `const workspaceRequest = fetchWorkspace()`, immediately start `void fetchRuntime()`, and retain the existing post-workspace polling decision on `workspaceRequest.then(...)`.
- [ ] Rerun the focused component test and the profiling harness with ten iterations into `frontend/tmp/perf-after-concurrent/`.
- [ ] Commit with a message explaining that serialized metadata unnecessarily extended the terminal critical path.

### Task 2: Suppress no-op runtime poll updates (`pbv4`)

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Test: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`

**Interfaces:**
- Consumes: `WorkspaceRuntimeState` responses from `getWorkspaceRuntime`.
- Produces: a stable runtime-state fingerprint scoped to workspace ID and fleet host; identical poll responses do not replace `runtime`, normalize layout, filter mounted sessions, or change the active tab.

- [ ] Add a component test that mounts an active terminal, returns the same runtime payload for the three-second poll, and asserts the terminal is not reconstructed and no layout-driven refresh/resize work is scheduled.
- [ ] Run the focused test and confirm it fails against unconditional runtime/layout assignment.
- [ ] Add a deterministic fingerprint for the JSON API response and return early after clearing the error when the fingerprint and workspace identity match the applied state.
- [ ] Verify a changed session generation still updates the runtime and existing launch/relaunch tests remain green.
- [ ] Rerun the focused suite and capture an idle browser trace spanning at least two poll intervals to verify no xterm redraw work appears.
- [ ] Commit the no-op suppression separately.

### Task 3: Bound readiness to the selected terminal font (`gqqx`)

**Files:**
- Modify: `frontend/src/lib/components/terminal/terminalFontFamily.ts`
- Modify: `frontend/src/lib/components/terminal/XtermTerminalPane.svelte`
- Test: `frontend/src/lib/components/terminal/terminalFontFamily.test.ts`
- Test: `frontend/src/lib/components/terminal/TerminalPane.test.ts`

**Interfaces:**
- Produces: `primaryTerminalFontFamily(fontFamily: string): string` and a 300 ms maximum initial font wait.
- Late selected-font completion triggers at most one atlas clear, fit, and refresh when the terminal had to start on fallback metrics.

- [ ] Add helper tests proving quoted family lists select only the first effective family and generic-only stacks remain valid.
- [ ] Add terminal tests with a deferred `document.fonts.load` promise proving construction occurs after the selected font resolves, occurs after the 300 ms bound when it does not, and late completion causes exactly one atlas rebuild.
- [ ] Run the focused tests and confirm they fail while the component awaits global `document.fonts.ready`.
- [ ] Replace the global wait with `document.fonts.load` for the primary family and representative glyphs, raced against the bound; guard late repaint against disposal and duplicate completion.
- [ ] Run the Svelte autofixer and focused terminal tests, then rerun the profiling harness.
- [ ] Commit separately, noting that the benchmark benefit is a tail-latency safeguard rather than a warm-path median win.

### Task 4: Make generic data-change refreshes route-aware (`1k3c`)

**Files:**
- Modify: `packages/ui/src/Provider.svelte`
- Test: `frontend/src/Provider.test.ts`

**Interfaces:**
- Consumes: the existing `getPage(): string` provider callback.
- Produces: `data_changed` refreshes only the store visible on pulls, issues, or activity routes; terminal/workspace and unrelated modes perform no pull/issue/activity request.

- [ ] Replace the existing all-store test with table-driven route cases covering `pulls`, `mobile-pulls`, `issues`, `mobile-issues`, `activity`, `mobile-activity`, `focus`, `terminal`, and `workspaces`.
- [ ] Run `node node_modules/vite-plus/bin/vp test frontend/src/Provider.test.ts` and confirm terminal/workspace cases fail because all three stores reload.
- [ ] Add a small route dispatcher in `Provider.svelte`; keep focus refreshing pulls and issues because `getPage` alone does not identify the focus item type.
- [ ] Rerun the focused test and full Vite+ suite.
- [ ] Use the copied-state browser and network log to verify a `data_changed` event on a terminal route transfers zero activity-feed bytes instead of 7.32 MB.
- [ ] Commit separately.

### Task 5: Forward available replay before tmux refresh (`wjtq`)

**Files:**
- Modify: `internal/server/workspace_runtime_terminal.go`
- Test: `internal/server/workspace_runtime_terminal_test.go` or the existing server API test covering runtime terminal attach.

**Interfaces:**
- Consumes: the buffered first value already queued on `localruntime.Attachment.Output` by `session.subscribe`.
- Produces: a bounded initial-output forwarding step that writes immediately available replay to the accepted WebSocket before the synchronous tmux refresh; the bridge continues with the same channel for subsequent output.

- [ ] Add a server test whose attachment has buffered replay and a refresh callback blocked on a channel; assert the WebSocket receives replay before refresh is released.
- [ ] Run the focused Go test with `-shuffle=on` and confirm it times out or observes refresh first.
- [ ] Add a non-blocking initial replay forward immediately after resize and before refresh. Preserve closed-channel handling and leave live output to `bridgeRuntimeAttachment`.
- [ ] Add an OTel event or child span around initial refresh only if the existing `terminal.attach` span cannot distinguish the remaining time; do not create long-lived spans.
- [ ] Run focused localruntime/server tests and the profiling harness for ordinary and alternate-screen scenarios.
- [ ] Commit separately.

### Task 6: Evaluate alternate-screen snapshot replay (`txa5`)

**Files:**
- Inspect: `internal/workspace/localruntime/manager.go`
- Inspect: `internal/workspace/manager.go`
- Potentially modify/test only if the telemetry demonstrates a reproducible blank or materially slower alternate-screen first paint.

**Interfaces:**
- Existing alternate-screen state is tracked by `session.alternateScreenActive`; ordinary replay is intentionally skipped while it is active.

- [ ] Compare at least ten ordinary and alternate-screen runs after Task 5, including route-to-first-bytes and route-to-first-paint distributions.
- [ ] Inspect the Chromium and Go traces for alternate-screen attaches that wait on refresh or lack initial output.
- [ ] If alternate-screen p95 remains materially worse or blank output is reproducible, first write a failing manager/server test and implement a bounded current-pane snapshot that cannot replay stale pre-alternate-screen output.
- [ ] If ordinary and alternate-screen distributions overlap and every run paints, leave code unchanged and close Kata with `--audit-no-change` plus profiling artifacts as evidence.

### Task 7: Remove workspace enrichment subprocesses from foreground responses (`acjr`)

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/huma_routes.go`
- Test: `internal/server/api_test.go`

**Interfaces:**
- Produces: base workspace responses synchronously from persisted summaries plus a bounded in-memory stale-while-revalidate enrichment cache for git divergence and tmux activity.
- `getWorkspace` and `listWorkspaces` return cached enrichment immediately and schedule one deduplicated background refresh when missing or stale.

- [ ] Add an HTTP-level test with blocking fake git/tmux probes asserting `/api/v1/workspaces` and `/api/v1/workspaces/{id}` return before those probes are released.
- [ ] Confirm the test fails while `toWorkspaceResponse` probes synchronously.
- [ ] Introduce a server-owned enrichment cache with timestamp, mutex, and in-flight dedup; keep the request response independent of refresh completion.
- [ ] Move tmux pruning into the same bounded background reconciliation path.
- [ ] Preserve the current response fields when cached data exists and allow empty enrichment on the first request after startup.
- [ ] Run focused server tests, repeated curl timings, allocation profiles, and the workspace-switch harness.
- [ ] Commit separately only if measured latency/allocation reduction justifies the cache complexity; otherwise record a no-change audit.

### Task 8: Evaluate retained terminal instances (`gjns`)

**Files:**
- Inspect: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Inspect: `frontend/src/lib/components/terminal/XtermTerminalPane.svelte`
- Modify only if the measured construction/remount cost justifies retained WebGL contexts.

- [ ] Record route-to-terminal-construction and route-to-first-paint after Tasks 1-7.
- [ ] Compare the maximum theoretical saving from eliminating remount against browser memory and WebGL context cost for retention sizes two and four using a throwaway local experiment.
- [ ] If terminal construction remains a material fraction of switch latency, write a failing component/e2e test and implement deterministic LRU eviction with `dispose()` on eviction.
- [ ] If construction is only a few milliseconds or retained contexts add disproportionate memory/GPU cost, delete the experiment and close Kata with `--audit-no-change` evidence.

### Task 9: Final verification and performance report

**Files:**
- Create: `docs/reports/2026-07-14-workspace-switch-performance.md`
- Update: Kata issues under epic `qyh6`.

- [ ] Run the full Vite+ test suite after the final frontend edit.
- [ ] Run affected Playwright profiling/e2e suites after the final shared fixture edit.
- [ ] Run all affected Go packages with `-shuffle=on`, then `make test-short`, `make vet`, and `make lint` if runtime permits.
- [ ] Run the final ten-iteration profile into `frontend/tmp/perf-workspace-final/` on the same browser/platform/renderer.
- [ ] Report median and p95 for workspace request end, runtime request start/end, terminal construction, socket open, first bytes, first paint, and byte-to-paint for ordinary and alternate-screen scenarios.
- [ ] Report copied-state workspace-list latency/allocation and activity bytes avoided on terminal routes.
- [ ] Close each completed Kata child immediately with its commit SHA; use typed `--audit-no-change` evidence for rejected investigations. Close epic `qyh6` only when all children are closed.
- [ ] Commit the report with the final verified measurements.
