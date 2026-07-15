# Workspace switch performance report

## Outcome

Workspace switching now reaches terminal content comfortably inside the one-second target in the profiling harness. The largest improvements are before terminal replay: workspace metadata is cache-only on the request path, metadata and runtime state start concurrently, selected-font readiness is bounded, and buffered terminal replay is forwarded before tmux reconciliation.

The remaining dominant phase is parsing and painting terminal bytes. Rare first-byte stalls still occur, but the two final 10-iteration captures stayed below 700 ms route-to-paint, including outliers.

## Provenance

| Capture | Commit | Browser | Renderer | Warm samples per scenario |
| --- | --- | --- | --- | ---: |
| Baseline | `772ff300` (PR #674) | Chromium 149.0.7827.55 | xterm | 10 |
| Final | `e90bea15` | Chromium 149.0.7827.55 | xterm | 20 across two captures |

Percentiles use the nearest-rank convention. Medians for even sample counts average the two middle values.

## Browser results

### Warm ordinary shell

| Phase from route selection | Baseline median | Final median | Median change | Baseline p95 | Final p95 | p95 change |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Workspace response | 23.9 ms | 2.5 ms | 89.5% lower | 36.3 ms | 4.5 ms | 87.6% lower |
| Terminal constructed | 29.2 ms | 5.7 ms | 80.6% lower | 40.6 ms | 8.8 ms | 78.3% lower |
| First terminal bytes | 52.5 ms | 29.4 ms | 44.1% lower | 61.2 ms | 33.5 ms | 45.3% lower |
| First terminal paint | 179.5 ms | 156.2 ms | 13.0% lower | 185.9 ms | 203.1 ms | 9.3% higher |

The final maximum was 505.7 ms, caused by one 363.0 ms first-byte outlier. The other 19 final samples reached first bytes in at most 33.5 ms.

### Warm alternate-screen application

| Phase from route selection | Baseline median | Final median | Median change | Baseline p95 | Final p95 | p95 change |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Workspace response | 22.4 ms | 2.3 ms | 89.7% lower | 44.6 ms | 4.0 ms | 91.0% lower |
| Terminal constructed | 27.1 ms | 5.6 ms | 79.3% lower | 49.6 ms | 8.1 ms | 83.7% lower |
| First terminal bytes | 49.1 ms | 29.5 ms | 40.0% lower | 72.1 ms | 43.7 ms | 39.4% lower |
| First terminal paint | 176.5 ms | 162.6 ms | 7.9% lower | 199.1 ms | 219.3 ms | 10.1% higher |

The final maximum was 691.2 ms, caused by one 534.3 ms first-byte outlier. The other 19 final samples reached first bytes in at most 43.7 ms.

First-bytes-to-paint is now the largest routine phase: approximately 126 ms median for ordinary shells and 135 ms for alternate-screen content. It did not improve materially in this pass and is the clearest follow-up target if further latency work is justified.

## Workspace API results

Repeated copied-state requests to `/api/v1/workspaces` measured the effect of moving git, tmux, and pruning probes out of the request path.

| Metric | Baseline | Final | Change |
| --- | ---: | ---: | ---: |
| Median | 68.082 ms | 2.367 ms | 96.5% lower |
| p95 | 77.431 ms | 9.719 ms | 87.4% lower |
| Mean | 68.453 ms | 3.506 ms | 94.9% lower |
| Positive allocation delta, 50-request process window | ~159.27 MB | 9.84 MB | 93.8% lower upper bound |

The final allocation number is a live-process upper bound. Unrelated background observers dominated the remaining sampled allocation, and request-handler nodes were below sampling resolution; it must not be presented as per-request allocation.

## Terminal retention audit

A synthetic localized-switch workload compared remounting with LRU retention sizes 2 and 4 across 60 switches.

| Retention | Hits | Overall median | p95 | Resident WebGL addons | Resident canvases | Resident canvas pixels |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0, remount | 0/60 | 165.7 ms | 191.8 ms | 0 | 0 | 0 |
| 2 | 14/60 | 163.1 ms | 191.5 ms | 2 | 4 | 2.41M |
| 4 | 47/60 | 25.4 ms | 195.1 ms | 4 | 9 | 4.82M |

Size 2 did not materially improve the workload. Size 4 made cache hits fast, but only by retaining parsed terminal state and four live WebGL contexts; p95 did not improve. The real harness constructs and attaches xterm in roughly 5–7 ms median after the other fixes, so the production benefit does not justify persistent GPU memory, hidden-terminal lifecycle work, socket/replay semantics, and eviction complexity. Current deterministic disposal remains the recommendation. The experiment verified that disposal returned the DOM canvas count to zero.

## Test-duration guardrail

The shared heavyweight workspace fixture disables background enrichment unless a test explicitly covers it. Production constructors leave enrichment enabled.

| Verification | Result |
| --- | ---: |
| Two clean final-tree `internal/server` runs | 332.60 s and 330.43 s |
| Five enrichment opt-in integration tests | 8.53 s test time |
| Focused enrichment race suite | 20.08 s test time |
| Hook-enforced repository short suite | Passed |

The clean package runs remain approximately at the prior 5m30s baseline. A later 394.36 s package run was excluded from duration comparison because another full server suite, Playwright, Vitest, and Svelte checks were running concurrently; it failed through subprocess-capacity and cleanup timeouts with 637.33 s of system CPU rather than product assertions.

## Implemented changes

- `60cd4b8f`: start workspace metadata and runtime requests concurrently.
- `919761c2`: skip unchanged runtime polling updates.
- `461ddef3`, `578aa9b6`: load only the selected terminal font with a bounded wait and synchronize late geometry changes.
- `1f790ffd`: avoid unrelated provider reloads outside active data views.
- `6011554a`: forward terminal replay before tmux refresh.
- `0ee0c9a5`: prevent stale runtime responses from overwriting local mutations.
- `5b1cb3f2`: serve cached workspace enrichment and reconcile it through bounded background workers.
- `e90bea15`: preserve the browser/backend profiling workflow as a reusable Codex skill.

## Artifacts

- Baseline browser samples: [2026-07-14-workspace-switch-baseline.txt](artifacts/2026-07-14-workspace-switch-baseline.txt)
- Final browser samples: [capture A](artifacts/2026-07-14-workspace-switch-final-a.txt) and [capture B](artifacts/2026-07-14-workspace-switch-final-b.txt)
- Terminal retention measurements: [2026-07-14-terminal-retention-summary.json](artifacts/2026-07-14-terminal-retention-summary.json)
- Profiling skill: `skills/profiling-middleman-performance/`

The full Chromium and Go traces are intentionally excluded because of their size. The checked-in browser-switch and terminal-retention samples preserve those report inputs. Workspace API timings, allocation profiles, and test-duration measurements came from live captures; the profiling command documents how to reproduce their raw inputs when deeper inspection is needed.
