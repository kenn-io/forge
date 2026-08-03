# Onboarding Sync Review Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent stale sync status from completing first-run onboarding and make mounted onboarding react when repository settings become configured.

**Architecture:** The sync store will establish a server-authoritative completion baseline before triggering whenever local status is absent, then reuse its centralized status guard for polling and SSE. The onboarding component will add a phase-guarded reactive edge transition that delegates to the existing `moveTo` and idempotent `startSync` functions.

**Tech Stack:** Svelte 5 runes, TypeScript, Vite+ test runner, Testing Library Svelte.

## Global Constraints

- Do not infer sync freshness from client wall-clock time.
- Keep polling and SSE completion behavior centralized in `applySyncStatus`.
- Start mounted onboarding sync exactly once when configured repositories transition from absent to present.
- Do not create a new roborev review.

---

### Task 1: Establish an authoritative sync baseline

**Files:**
- Modify: `packages/ui/src/stores/sync.svelte.test.ts`
- Modify: `packages/ui/src/stores/sync.svelte.ts`

**Interfaces:**
- Consumes: `ForgeClient.GET("/sync/status")`, `ForgeClient.POST("/sync", options)`, and the existing `applySyncStatus(next)` completion path.
- Produces: `runTriggeredSync(request)` that preloads status when `getSyncState()` is `null` and never treats an idle timestamp as advanced without a known baseline.

- [ ] **Step 1: Write the failing null-status regression**

Add a test to `packages/ui/src/stores/sync.svelte.test.ts` that begins without `setSyncStatus`, returns the same historical idle status for both pre-trigger and immediate post-trigger reads, and verifies the optimistic run remains active:

```ts
it("does not complete a triggered sync from stale idle status when local status was null", async () => {
  const staleLastRunAt = "2026-08-02T19:00:00Z";
  const get = vi.fn(async (path: string) => {
    if (path === "/sync/status") {
      return {
        data: { running: false, last_run_at: staleLastRunAt, last_error: "" },
      };
    }
    return { data: { provider_pools: {}, local_ceilings: {} } };
  });
  const store = createSyncStore({
    client: {
      GET: get,
      POST: vi.fn(async () => ({ error: undefined })),
    } as unknown as ForgeClient,
  });
  const completed = vi.fn();
  store.subscribeSyncComplete(completed);

  await store.triggerSync();

  expect(get.mock.calls.filter(([path]) => path === "/sync/status")).toHaveLength(2);
  expect(store.getSyncState()).toEqual({
    running: true,
    last_run_at: staleLastRunAt,
    last_error: "",
  });
  expect(completed).not.toHaveBeenCalled();
});
```

- [ ] **Step 2: Run the focused store test and verify RED**

Run:

```bash
node node_modules/vite-plus/bin/vp test packages/ui/src/stores/sync.svelte.test.ts --run
```

Expected: the new test fails because only the post-trigger status read occurs and its historical timestamp is accepted against the empty baseline.

- [ ] **Step 3: Implement the minimal baseline load**

In `packages/ui/src/stores/sync.svelte.ts`, add a focused status reader that returns a valid status without applying it:

```ts
async function fetchSyncStatus(): Promise<SyncStatus | null> {
  const { data, error } = await apiClient.GET("/sync/status");
  return !error && data ? data : null;
}
```

At the beginning of `runTriggeredSync`, establish the baseline before publishing optimistic running state:

```ts
const previous = status ?? (await fetchSyncStatus());
const previousLastRunAt = previous?.last_run_at ?? null;

sseGeneration++;
triggeredSyncLastRunAt = previousLastRunAt;
status = {
  running: true,
  last_run_at: previousLastRunAt ?? "",
  last_error: "",
};
```

Change `triggeredSyncLastRunAt` to distinguish no guard from an unknown baseline, for example with `undefined` as inactive and `null` as active-but-unknown. In `applySyncStatus`, clear the guard when running is observed; accept idle completion only when the baseline is non-null and `last_run_at` advances. On trigger failure, restore `previous` values and deactivate the guard.

- [ ] **Step 4: Run the focused store test and verify GREEN**

Run:

```bash
node node_modules/vite-plus/bin/vp test packages/ui/src/stores/sync.svelte.test.ts --run
```

Expected: all sync-store tests pass, including fast completion with a known baseline and stale idle status with initially null local status.

- [ ] **Step 5: Format the sync files**

Run:

```bash
node node_modules/vite-plus/bin/vp fmt packages/ui/src/stores/sync.svelte.ts packages/ui/src/stores/sync.svelte.test.ts --threads=1
```

Expected: both files are formatted without errors.

### Task 2: Advance mounted onboarding after settings configuration

**Files:**
- Modify: `frontend/src/lib/components/onboarding/OnboardingFlow.test.ts`
- Modify: `frontend/src/lib/components/onboarding/OnboardingFlow.svelte`

**Interfaces:**
- Consumes: reactive `stores.settings.hasConfiguredRepos()`, existing `moveTo(next: Phase)`, and idempotent `startSync()`.
- Produces: one transition from repository selection to first sync when configured repositories appear after mount.

- [ ] **Step 1: Make the settings fixture reactive and write the failing transition test**

Update the test fixture's configuration state to use Svelte reactive state so the mounted component observes changes:

```ts
let configured = $state(options.configured ?? false);
```

Keep `setConfiguredRepos` assigning `configured = true`, and return an explicit test-only setter from the fixture:

```ts
const configureExternally = () => {
  configured = true;
};

return { stores, setConfiguredRepos, triggerSync, loadPulls, configureExternally };
```

Add the regression:

```ts
it("starts sync when repositories become configured while onboarding remains mounted", async () => {
  const fixture = storeFixture();
  renderFlow(fixture.stores);

  await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));
  await waitFor(() =>
    expect(screen.getByRole("heading", { name: "Choose the repositories you maintain" })).toBeTruthy(),
  );

  fixture.configureExternally();

  await waitFor(() => expect(fixture.triggerSync).toHaveBeenCalledOnce());
  expect(screen.queryByRole("heading", { name: "Choose the repositories you maintain" })).toBeNull();
});
```

- [ ] **Step 2: Run the focused onboarding test and verify RED**

Run:

```bash
node node_modules/vite-plus/bin/vp test frontend/src/lib/components/onboarding/OnboardingFlow.test.ts --run
```

Expected: the new test fails because `activeStep` advances but `phase` remains `"repos"`, leaving no sync start.

- [ ] **Step 3: Implement the guarded reactive phase transition**

In `frontend/src/lib/components/onboarding/OnboardingFlow.svelte`, track the last repository-configuration state and react only to the false-to-true edge:

```ts
let hadConfiguredRepos = untrack(() => stores.settings.hasConfiguredRepos());

$effect(() => {
  const configured = hasConfiguredRepos;
  const becameConfigured = configured && !hadConfiguredRepos;
  hadConfiguredRepos = configured;
  if (!becameConfigured || phase !== "repos") return;
  void moveTo("sync").then(startSync);
});
```

Keep the existing `onMount` initial-sync behavior unchanged. `startSync` remains the duplicate-start guard.

- [ ] **Step 4: Run the Svelte autofixer**

Run:

```bash
node node_modules/vite-plus/bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/onboarding/OnboardingFlow.svelte --svelte-version 5
```

Expected: no correctness issues from the new effect. Evaluate generic warnings about the existing async repository-loading effect separately; do not widen scope.

- [ ] **Step 5: Run the focused onboarding test and verify GREEN**

Run:

```bash
node node_modules/vite-plus/bin/vp test frontend/src/lib/components/onboarding/OnboardingFlow.test.ts --run
```

Expected: all onboarding component tests pass and the new test observes one sync trigger.

- [ ] **Step 6: Format the onboarding files**

Run:

```bash
node node_modules/vite-plus/bin/vp fmt frontend/src/lib/components/onboarding/OnboardingFlow.svelte frontend/src/lib/components/onboarding/OnboardingFlow.test.ts --threads=1
```

Expected: both files are formatted without errors.

### Task 3: Verify and deliver the review fixes

**Files:**
- Verify: `packages/ui/src/stores/sync.svelte.ts`
- Verify: `packages/ui/src/stores/sync.svelte.test.ts`
- Verify: `frontend/src/lib/components/onboarding/OnboardingFlow.svelte`
- Verify: `frontend/src/lib/components/onboarding/OnboardingFlow.test.ts`

**Interfaces:**
- Consumes: completed tasks 1 and 2.
- Produces: a tested commit pushed to the existing PR branch.

- [ ] **Step 1: Run both focused suites together**

Run:

```bash
node node_modules/vite-plus/bin/vp test packages/ui/src/stores/sync.svelte.test.ts frontend/src/lib/components/onboarding/OnboardingFlow.test.ts --run
```

Expected: both files pass.

- [ ] **Step 2: Run the full frontend unit suite**

Run:

```bash
node node_modules/vite-plus/bin/vp test --run
```

Expected: the complete frontend and package unit suite passes with only established skips.

- [ ] **Step 3: Run frontend lint and Svelte diagnostics**

Run:

```bash
node node_modules/vite-plus/bin/vp run frontend-package-typecheck
```

Expected: Svelte diagnostics report zero errors and zero warnings; existing lint warnings outside the changed files may be reported but must not increase.

- [ ] **Step 4: Verify formatting and patch hygiene**

Run:

```bash
node node_modules/vite-plus/bin/vp fmt --check frontend/src/lib/components/onboarding/OnboardingFlow.svelte frontend/src/lib/components/onboarding/OnboardingFlow.test.ts packages/ui/src/stores/sync.svelte.ts packages/ui/src/stores/sync.svelte.test.ts --threads=1
git diff --check
```

Expected: formatting and whitespace checks pass.

- [ ] **Step 5: Run context synchronization before commit**

Read and follow `.agents/skills/context-sync/SKILL.md`, then invoke commit mode for the intended diff. Run `scripts/context-sync --check` and update context only if the implementation introduces a durable invariant not already captured by the approved design or existing ADR.

- [ ] **Step 6: Commit through the mandatory commit workflow**

Read and follow the mandatory `commit` skill. Stage only the four implementation/test files plus any clear context update produced by Step 5. Use a rationale-focused conventional commit such as:

```bash
git commit -m "fix: preserve onboarding sync authority" \
  -m "First-run sync must not complete from historical status when the client has not loaded a baseline, and mounted onboarding must react when settings become configured elsewhere. Establish server authority before triggering and advance the existing flow on the configuration edge so both paths start exactly once." \
  -m $'Generated with pi\nCo-authored-by: pi <noreply@openai.com>'
```

- [ ] **Step 7: Scrub and push the existing PR branch**

Run the repository's public-diff private-data scrub workflow against the new commit, then:

```bash
git push origin HEAD
git status --short --branch
```

Expected: the push updates `t3code/design-onboarding-mockups`, and the working tree is clean and synchronized with upstream. Do not invoke `roborev review` or any review-producing roborev skill.
