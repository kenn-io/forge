# Adaptive Roborev Recovery Polling Implementation Plan

> **For agentic workers:** Follow this plan task by task using test-driven development. Preserve the existing false-to-true recovery callback contract.

**Goal:** Detect a stopped Roborev daemon’s recovery promptly without generating event-stream proxy noise while the daemon is known to be unavailable.

**Architecture:** Replace the fixed health interval with a self-scheduling timeout that polls every second while unavailable and every 30 seconds while healthy. Guard each polling generation so a stale in-flight request cannot reschedule after stop or restart. Make the reviews event stream reactive to daemon availability, and make the recovery E2E exercise automatic recovery without clicking Retry.

**Tech Stack:** Svelte 5 runes, TypeScript, Vite+, Vitest fake timers, Playwright, Docker Compose.

## Global Constraints

- Use Bun/Vite+ commands only; do not run npm.
- Keep healthy polling at 30 seconds and unavailable polling at 1 second.
- Preserve `onRecover` on every false-to-true transition, including first recovery.
- Do not connect the jobs event stream while health reports unavailable.
- Use the Svelte autofixer before finalizing `ReviewsView.svelte`.
- Run `skills/context-sync/SKILL.md --commit` workflow before every commit.
- Do not weaken Playwright timeouts to hide recovery latency.

---

### Task 1: Add adaptive, generation-safe health polling

**Files:**

- Create: `packages/ui/src/stores/roborev/daemon.svelte.test.ts`
- Modify: `packages/ui/src/stores/roborev/daemon.svelte.ts`

**Step 1: Add a failing store test**

Create a focused test using `vi.useFakeTimers()` and a mocked `MiddlemanClient.GET` whose health responses are:

1. `{ available: false, version: "", endpoint: "http://roborev:7373" }`
2. `{ available: true, version: "v0.52.0", endpoint: "http://roborev:7373" }`
3. healthy responses thereafter

Mock the direct Roborev client’s `/api/status` response with the required numeric counters. Start polling and assert:

- the first health request makes the store unavailable;
- advancing 999 ms does not issue another health request;
- advancing the final 1 ms issues the second request;
- the store becomes available and calls `onRecover` once;
- no further health request occurs before 30 seconds;
- the next health request occurs at the healthy 30-second cadence.

Always call `store.stopPolling()` and `vi.useRealTimers()` in cleanup.

**Step 2: Run the focused test and confirm the old cadence fails**

From `frontend/`, run:

```bash
../node_modules/.bin/vp test run --project unit ../packages/ui/src/stores/roborev/daemon.svelte.test.ts
```

Expected: FAIL because the fixed interval does not recheck after one second.

**Step 3: Implement adaptive self-scheduling polling**

In `daemon.svelte.ts`:

- define 1-second unavailable and 30-second available interval constants;
- change `pollHandle` to `ReturnType<typeof setTimeout> | null`;
- add a monotonically increasing `pollGeneration`;
- add an async poll function that:
  1. awaits `checkHealth()`;
  2. returns if its captured generation is stale;
  3. refreshes status when available;
  4. schedules the next timeout using the current availability;
- make `startPolling()` invalidate any old generation, capture the new one, and start immediately;
- make `stopPolling()` increment the generation and clear the timeout with `clearTimeout`.

Do not change `checkHealth()`’s false-to-true `loadStatus`/`onRecover` behavior.

**Step 4: Run focused and full unit verification**

From `frontend/`, run:

```bash
../node_modules/.bin/vp test run --project unit ../packages/ui/src/stores/roborev/daemon.svelte.test.ts
../node_modules/.bin/vp test run --project unit
```

Expected: PASS.

**Step 5: Run context sync and commit**

Follow `skills/context-sync/SKILL.md --commit`, including:

```bash
scripts/context-sync --check
```

Then inspect the intended diff and commit with a body that records the tradeoff:

```bash
git add packages/ui/src/stores/roborev/daemon.svelte.ts packages/ui/src/stores/roborev/daemon.svelte.test.ts
git commit -m "fix: recover Roborev health promptly" -m "Poll once per second only while the daemon is unavailable, then return to the existing 30-second healthy cadence. A generation token prevents stale in-flight checks from restarting polling after stop or restart."
```

---

### Task 2: Gate the event stream and prove automatic recovery end to end

**Files:**

- Modify: `frontend/tests/e2e-full/roborev-e2e.spec.ts`
- Modify: `packages/ui/src/views/ReviewsView.svelte`

**Step 1: Rewrite the recovery E2E first**

Change `status strip shows connected after recovery` so it is self-contained:

1. stop the daemon;
2. navigate to `/reviews`;
3. assert the unavailable empty state;
4. start the daemon without clicking Retry;
5. assert the empty state disappears;
6. assert `.conn-indicator.connected` becomes visible;
7. assert at least one job row appears.

Keep the existing 15–20 second assertion windows. The old 30-second health interval should make this fail deterministically.

**Step 2: Run the Roborev E2E and confirm the recovery test fails**

Run:

```bash
make test-e2e-roborev
```

Expected: FAIL at the automatic recovery assertion before the store implementation is present. If Task 1 is already implemented, inspect the test logic and continue; do not temporarily revert correct code just to manufacture a red run.

**Step 3: Make the event stream availability-reactive**

In `ReviewsView.svelte`:

- keep `onMount` responsible only for the initial job load when already available;
- replace the unconditional event-stream connection and `onDestroy` cleanup with a `$effect`;
- have the effect return immediately unless both the jobs store exists and the daemon is available;
- compute the existing base path, connect the stream, and return cleanup that disconnects it.

The effect must disconnect when availability flips false and reconnect when it flips true. Remove the unused `onDestroy` import.

**Step 4: Run the Svelte autofixer**

From the repository root, use the repo-required Svelte tool:

```bash
node node_modules/vite-plus/bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/views/ReviewsView.svelte
```

Apply any relevant fixes and rerun until it reports no applicable issues.

**Step 5: Run all affected verification**

From `frontend/`, run:

```bash
../node_modules/.bin/vp test run --project unit
```

From the repository root, run:

```bash
make test-e2e-roborev
git diff --check
```

Expected: full unit suite and full Roborev Playwright project PASS.

**Step 6: Run context sync and commit**

Follow `skills/context-sync/SKILL.md --commit`, including `scripts/context-sync --check`. Confirm no durable context change is needed unless the new behavior exposes a missing cross-cutting invariant.

Commit:

```bash
git add packages/ui/src/views/ReviewsView.svelte frontend/tests/e2e-full/roborev-e2e.spec.ts
git commit -m "fix: reconnect Roborev reviews after recovery" -m "Keep the event stream disconnected while health reports the daemon unavailable, then reconnect reactively once adaptive polling observes recovery. The E2E now owns its stop-start lifecycle and verifies recovery without a manual Retry."
```

**Step 7: Final branch review**

Inspect:

```bash
git status --short
git log --oneline origin/main..HEAD
git diff --stat origin/main...HEAD
git diff origin/main...HEAD
```

Confirm the branch contains only the approved design, implementation plan, adaptive poller, store test, availability-gated event stream, and self-contained recovery E2E.
