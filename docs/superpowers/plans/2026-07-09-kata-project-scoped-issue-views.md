# Kata Project-Scoped Issue Views Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure project-scoped Kata views render only rows returned by the selected project's issue-list endpoint.

**Architecture:** Keep unscoped views on the generic issue list and preserve their parallel project/list loading. Resolve a concrete starting daemon for issue, search, and multi-request create operations; use the same effective daemon selector for related reads and mutations. For scoped views, resolve `project_uid` from that daemon's project catalog, pass the project into `fetchIssuesByStatus`, and treat an unknown UID as an empty scope.

**Tech Stack:** TypeScript, Vite+ unit tests, Playwright full-stack e2e fixture.

## Global Constraints

- Do not add a compatibility shim or new API route.
- Preserve generic all-project loading for queries without `project_uid`.
- Preserve the bounded `limit=500` query for logbook views.
- Pin every multi-request issue/search operation to its concrete starting daemon, including the roster's default ID when no daemon is explicitly active.
- Run the full frontend Vite+ suite and affected Playwright Kata test after the final edit.

---

### Task 1: Scope issue-view transport by project

**Files:**

- Modify: `frontend/src/lib/api/kata/taskClient.ts:572`
- Test: `frontend/src/lib/api/kata/taskClient.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

**Interfaces:**

- Consumes: `fetchProjects(daemonId?)` and `fetchIssuesByStatus(status, daemonId?, project?)`.
- Produces: unchanged `KataTaskAPI.issues(query): Promise<KataTaskViewResponse>` behavior with project-aware transport selection.

- [x] **Step 1: Add failing client tests for known and unknown project UIDs**

Replace the existing project/area view test with a known-project transport test and add an unknown-project test:

```typescript
test("loads project-scoped issue views through the project issue list", async () => {
  const healthProject = { ...project("project-health", "Health", { area: "Personal" }), id: 7 };
  const { calls, fetchImpl } = createFetchStub({
    "/api/v1/projects?include=stats": { body: { projects: [healthProject] } },
    "/api/v1/issues?status=open": {
      body: { issues: [issue("issue-contaminating", "Contaminating task", "project-health")] },
    },
    "/api/v1/projects/7/issues?status=open": {
      body: { issues: [issue("issue-health", "Health task", "project-health")] },
    },
  });
  const api = createKataTaskAPI({ fetchImpl });

  const view = await api.issues({ view: "all", area: "Personal", project_uid: "project-health" });

  expect(view.groups.flatMap((group) => group.issues).map((item) => item.title)).toEqual(["Health task"]);
  expect(calls.map((call) => proxyPath(call.url))).toEqual([
    "/api/v1/projects?include=stats",
    "/api/v1/projects/7/issues?status=open",
  ]);
});

test("returns an empty issue view for an unknown project UID", async () => {
  const { calls, fetchImpl } = createFetchStub({
    "/api/v1/projects?include=stats": {
      body: { projects: [project("project-work", "Work")] },
    },
  });
  const api = createKataTaskAPI({ fetchImpl });

  const view = await api.issues({ view: "all", project_uid: "project-missing" });

  expect(view.groups).toEqual([]);
  expect(calls.map((call) => proxyPath(call.url))).toEqual(["/api/v1/projects?include=stats"]);
});
```

- [x] **Step 2: Run the focused client test and verify the new tests fail for the transport bug**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/api/kata/taskClient.test.ts
```

Expected: the known-project test renders `Contaminating task` or requests `/api/v1/issues`, and the unknown-project test rejects after requesting the unhandled generic route.

- [x] **Step 3: Strengthen the full-stack fixture and scoped-route test**

Add `genericIssues?: IssueSummary[]` to `KataBackendOptions` and `BackendState`, initialize it from `options.genericIssues`, and serve `state.genericIssues ?? state.issues` from the generic `/api/v1/issues` case so existing tests retain live issue mutations. In the existing scoped-route test, pass a same-project contaminating row through `genericIssues` and assert it is absent after the project row renders:

```typescript
const contaminatingIssue = {
  ...issues[1]!,
  uid: "issue-generic-contamination",
  short_id: "kat-contamination",
  qualified_id: "Kata#kat-contamination",
  title: "Generic list contamination",
};
const backend = await startKataBackend({ genericIssues: [contaminatingIssue] });
```

```typescript
await expect(taskList.getByRole("button", { name: /Generic list contamination/ })).toHaveCount(0);
```

- [x] **Step 4: Run the focused Playwright test and verify it fails before production code changes**

Run:

```bash
cd frontend && MIDDLEMAN_E2E_OUTPUT_FILE=../tmp/kata-project-scope-red.log node ./scripts/run-e2e-to-file.ts --project=chromium tests/e2e-full/kata.spec.ts -g "project filter without a query"
```

Expected: FAIL because the generic route is requested and its contaminating row is rendered.

- [x] **Step 5: Implement project-aware issue-view loading**

Replace the unconditional issue-list promise in `issues()` with project-aware selection while retaining parallel loading for unscoped views:

```typescript
const daemonId = await resolveOperationDaemonId();
const status = query.view === "logbook" ? "closed" : "open";
const genericIssuesPromise =
  query.project_uid === undefined ? fetchIssuesByStatus(status, daemonId, undefined, true) : undefined;
const projectsPromise = fetchProjects(daemonId, true);
const issuesPromise =
  genericIssuesPromise ??
  projectsPromise.then((projects) => {
    const project = projects.projects.find((item) => item.uid === query.project_uid);
    return project ? fetchIssuesByStatus(status, daemonId, project, true) : [];
  });
const [issues, projects] = await Promise.all([issuesPromise, projectsPromise]);
```

Keep the existing project map, local `issueMatchesScope` filter, and `buildKataTaskView` call unchanged.

- [x] **Step 6: Run focused unit and Playwright tests and verify they pass**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/api/kata/taskClient.test.ts
cd frontend && MIDDLEMAN_E2E_OUTPUT_FILE=../tmp/kata-project-scope-green.log node ./scripts/run-e2e-to-file.ts --project=chromium tests/e2e-full/kata.spec.ts -g "project filter without a query"
```

Expected: both commands exit zero.

- [x] **Step 7: Run full affected frontend verification**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test
cd frontend && MIDDLEMAN_E2E_OUTPUT_FILE=../tmp/kata-e2e.log node ./scripts/run-e2e-to-file.ts tests/e2e-full/kata.spec.ts
```

Expected: both commands exit zero.

- [x] **Step 8: Commit the review fix**

Stage only the plan, client, client test, and Kata e2e spec, then create a hook-enforced conventional commit explaining that scoped routed views previously still consumed the generic list.

### Task 2: Pin multi-request view and search operations

**Files:**

- Modify: `frontend/src/lib/api/kata/taskClient.ts:41-45,258-270,343-475,584-622`
- Test: `frontend/src/lib/api/kata/taskClient.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`
- Modify: `docs/superpowers/specs/2026-07-09-kata-project-scoped-issue-views-design.md`

**Interfaces:**

- Consumes: `getActiveKataDaemon()`, `getDefaultKataDaemon()`, and `fetchKataDaemons()`.
- Produces: operation-wide concrete daemon pinning for `KataTaskAPI.issues()` and `KataTaskAPI.search()`.

- [x] **Step 1: Add failing unit regressions for issue views, searches, and label hydration**

Add `getDefaultDaemonId?: () => string | undefined` to the test-injectable client options. In `taskClient.test.ts`, use fetch wrappers that change the active or default getter after the catalog response and assert the captured headers:

```typescript
expect(issueViewCalls.map((call) => call.headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "home"]);
expect(projectSearchCalls.map((call) => call.headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "home"]);
expect(labelHydrationCalls.map((call) => call.headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "home", "home"]);
```

The label-hydration case must use `query: "rent"`, `label: "money"`, a search response without labels, and `/api/v1/projects/1/issues?status=open` returning the `money` label.

Add public-API readiness cases with both getters returning `undefined`: one roster response with default daemon `home` must produce request paths `[/api/v1/kata/daemons, /api/v1/projects?include=stats, /api/v1/projects/1/issues?status=open]` and headers `[null, home, home]`; an empty roster must reject before project reads with status `503` and code `service_unavailable`.

- [x] **Step 2: Run the focused unit regressions and verify they fail on daemon drift**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/api/kata/taskClient.test.ts -t "pins project-scoped|hydrates labels"
```

Expected before the fix: later requests carry `work` or an empty header instead of the starting concrete `home` ID.

- [x] **Step 3: Add a failing two-daemon full-stack default-change regression**

Extend `BackendState` and `KataBackendOptions` with `projectsBarrier?: Promise<void>` and await it before the fixture returns `GET /api/v1/projects`. Start `home` and `work` backends with the same project ID/UID but distinct rows, configure `home` as the default, navigate directly to that project scope, and wait until the home catalog request stalls. Rewrite the Kata config with `work` as `active_daemon`, release the barrier, then assert:

```typescript
await expect(taskList.getByRole("button", { name: /Home scoped task/ })).toBeVisible();
await expect(taskList.getByRole("button", { name: /Foreign work task/ })).toHaveCount(0);
await expect.poll(() => home.state.seenPaths).toContain("GET /api/v1/projects/7/issues?status=open");
expect(work.state.seenPaths).not.toContain("GET /api/v1/projects/7/issues?status=open");
```

- [x] **Step 4: Run the full-stack regression and verify the foreign row wins before the fix**

Run:

```bash
cd frontend && MIDDLEMAN_E2E_OUTPUT_FILE=../tmp/kata-default-daemon-red.log node ./scripts/run-e2e-to-file.ts --project=chromium tests/e2e-full/kata.spec.ts -g "starting default daemon when configuration changes"
```

Expected before the fix: FAIL because `Foreign work task` renders after the server resolves the changed default for the project-list request.

- [x] **Step 5: Resolve the concrete daemon and propagate pinned headers**

Resolve the operation daemon once at both public entry points. The resolver must never return an empty daemon ID:

```typescript
async function resolveOperationDaemonId(explicitDaemonId?: string): Promise<string> {
  const selected = explicitDaemonId?.trim() || getDaemonId()?.trim() || getDefaultDaemonId()?.trim();
  if (selected) return selected;

  const daemons = await fetchKataDaemons(baseFetchImpl);
  const fallback = daemons.find((daemon) => daemon.default) ?? daemons[0];
  if (fallback?.id) return fallback.id;

  throw new KataTaskAPIError({
    status: 503,
    code: "service_unavailable",
    message: "no Kata daemon is available",
    headers: new Headers(),
  });
}
```

Thread `pinned = true` through `fetchProjects`, `fetchIssuesByStatus`, `searchAllProjects`, `searchProjectIssueList`, `searchProject`, and `hydrateProjectSearchRows`. Each request must choose `pinnedDaemonHeaders(daemonId)` when pinned so catalog, open/closed lists, text search, and label hydration share the same concrete header.

- [x] **Step 6: Run focused and full verification**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/api/kata/taskClient.test.ts
cd frontend && MIDDLEMAN_E2E_OUTPUT_FILE=../tmp/kata-default-daemon-green.log node ./scripts/run-e2e-to-file.ts --project=chromium tests/e2e-full/kata.spec.ts -g "starting default daemon when configuration changes"
cd frontend && ../node_modules/.bin/vp test --maxWorkers=2
cd frontend && MIDDLEMAN_E2E_OUTPUT_FILE=../tmp/kata-e2e.log node ./scripts/run-e2e-to-file.ts tests/e2e-full/kata.spec.ts
```

Expected: all commands exit zero; the full Vite+ and complete Chromium/Firefox Kata suites pass.

- [x] **Step 7: Commit the daemon-pinning follow-up**

Stage the client, unit tests, Kata full-stack spec, design, and plan. Create a hook-enforced conventional commit explaining that concrete daemon IDs prevent numeric project IDs from crossing daemon boundaries.

### Task 3: Keep detail and mutation requests on the loaded daemon

**Files:**

- Modify: `frontend/src/lib/api/kata/taskClient.ts:258-290,560`
- Test: `frontend/src/lib/api/kata/taskClient.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`
- Modify: `docs/superpowers/specs/2026-07-09-kata-project-scoped-issue-views-design.md`

**Interfaces:**

- Consumes: active selection, stored roster default, and the existing on-demand roster resolver.
- Produces: one effective daemon selector for all client requests and concrete operation pinning for `createIssue()`.

- [x] **Step 1: Add failing client regressions**

Add tests proving an unpinned detail read uses the stored default and a create-plus-metadata operation with no ready getters first loads the roster, then sends both mutations with the resolved daemon header. Add matching on-demand roster and empty-roster cases for project search.

- [x] **Step 2: Extend the two-daemon browser regression**

After the starting daemon's scoped row renders, select it and assert its body loads from the starting backend. Verify the starting backend sees `GET /api/v1/issues/{uid}` and the newly configured backend does not.

- [x] **Step 3: Unify effective selection and creation pinning**

Use `getActiveDaemon() ?? getStoredDefault() ?? resolvedOnDemandDefault` as the selector passed to `withKataDaemon`. Cache a roster fallback only when neither getter supplies an ID. Resolve `createIssue()` through `resolveOperationDaemonId()` before its first mutation so the create and metadata requests cannot cross daemon boundaries.

- [x] **Step 4: Run final affected verification and commit**

Run the complete task-client unit suite, full Vite+ suite, and complete Chromium/Firefox Kata Playwright spec. Commit only after all three pass.

### Task 4: Preserve the daemon associated with loaded results

**Files:**

- Modify: `frontend/src/lib/api/kata/taskClient.ts:260-290,604-640`
- Modify: `frontend/src/lib/api/kata/taskTypes.ts`
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.ts`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Test: `frontend/src/lib/api/kata/taskClient.test.ts`
- Test: `frontend/src/lib/stores/kata-workspace.svelte.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`
- Modify: `docs/superpowers/specs/2026-07-09-kata-project-scoped-issue-views-design.md`

**Interfaces:**

- Consumes: the concrete daemon resolved for issue views and non-explicit searches.
- Produces: daemon provenance on list/search results and an accepted workspace binding that related detail, event, project, and mutation requests use ahead of later active/default changes.

- [x] **Step 1: Add a failing client regression for post-load default drift**

Load a project view on `home`, change the injected stored-default getter to `work`, then assert detail and close requests for the loaded row retain the `home` header.

- [x] **Step 2: Make the browser roster race observable**

Delay the app-level roster request while Kata workspace bootstraps on `home`. Start a scoped view and stall its project catalog request, change the server default to `work`, then release the app roster so the browser's visible effective default changes before the home-scoped response completes. Use colliding issue UIDs on both daemons and assert the selected detail request header and completion mutation remain on `home`.

- [x] **Step 3: Add a replaceable workflow daemon pin**

Return `daemon_id` on issue-view and search responses. After request-generation checks reject stale results, the workspace store binds the accepted daemon into the client and exposes it to live-stream setup. Related requests select bound workflow daemon, active daemon, stored default, then on-demand fallback. New view/search resolution ignores the old workflow pin so an intentional active/default reload can replace it after acceptance; external shared-client searches do not bind themselves.

- [x] **Step 4: Pin multi-page event walks**

Resolve the effective workflow daemon once at the start of `events()` and send that concrete header on every page. Change the default getter after the first page in the pagination regression and assert both requests retain the starting daemon.

- [x] **Step 5: Run final verification and commit**

Run the complete task-client unit suite, full Vite+ suite, and complete Chromium/Firefox Kata Playwright spec before committing.

### Task 5: Keep explicit daemon-switch bootstrap coherent

**Files:**

- Modify: `frontend/src/lib/stores/kata-workspace.svelte.ts`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Test: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

- [x] **Step 1: Reproduce mixed daemon bootstrap**

Assert the switch clears the client binding and that the full-stack sidebar replaces the old daemon's project catalog alongside the new task rows.

- [x] **Step 2: Clear old daemon state at the explicit switch boundary**

After invalidating pending loads, clear projects, rows, selection, caches, cursor, and workflow binding before setting the new active daemon. Clear any partial replacement again before restoring the captured previous daemon and selection; leave state empty and the stream stopped if restoration also fails.

- [x] **Step 3: Run final verification and commit**

Run the full affected frontend and Kata Playwright suites before committing.

### Task 6: Keep workspace follow-up operations on accepted provenance

**Files:**

- Modify: `frontend/src/App.svelte`
- Modify: `frontend/src/lib/api/kata/taskClient.ts`
- Modify: `frontend/src/lib/stores/kata-workspace.svelte.ts`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Test: `frontend/src/App.test.ts`
- Test: `frontend/src/lib/stores/kata-workspace.svelte.test.ts`
- Test: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

- [x] **Step 1: Reproduce browser-default drift and the switch transition window**

Release an app-owned roster request after the configured default changes, then prove mutation/event refreshes and workspace creation would leave accepted provenance. Stall an explicit switch and prove old project and row controls remain visible.

- [x] **Step 2: Pin workspace ownership without affecting other surfaces**

Give Kata workspace its own task client. Pass its accepted daemon explicitly to workspace-owned list/search reloads and use the same identity for workspace creation while palette, Docs, and Messages keep a separate client.

- [x] **Step 3: Make explicit switch state and rollback atomic**

Clear daemon-scoped display state before changing the selector. On replacement failure, clear partial replacement state before restoration; restart the previous stream only after restoration succeeds.

- [x] **Step 4: Run final verification and commit**

Run the full Vite+ suite and complete Chromium/Firefox Kata Playwright spec before committing.

### Task 7: Align selector and mutation lifecycle with accepted provenance

**Files:**

- Modify: `frontend/src/lib/stores/kata-workspace.svelte.ts`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/features/kata/KataDaemonSwitcher.svelte`
- Test: `frontend/src/lib/stores/kata-workspace.svelte.test.ts`
- Test: `frontend/src/lib/features/kata/KataDaemonSwitcher.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

- [x] **Step 1: Keep selector identity on the accepted workspace daemon**

Render and compare explicit choices against accepted provenance. Capture that daemon for rollback even when a refreshed roster advertises another default.

- [x] **Step 2: Block switching through mutation and switch transitions**

Track task mutations through their post-mutation refresh and disable the selector while mutation, view, or switch work is active, including overlapping selector choices.

- [x] **Step 3: Cover drifted selection and terminal rollback failure**

After browser-side default drift, fail a third-daemon switch and prove rollback restores the accepted daemon before a real switch to the displayed default. Also prove double failure leaves empty state, a stopped stream, and a visible connection error.

- [x] **Step 4: Run final verification and commit**

Run the full Vite+ suite and complete Chromium/Firefox Kata Playwright spec before committing.

### Task 8: Cover removed provenance and every visible work gate

**Files:**

- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/features/kata/KataDaemonSwitcher.svelte`
- Test: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
- Test: `frontend/src/lib/features/kata/KataDaemonSwitcher.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

- [x] **Step 1: Preserve an accepted daemon that leaves the roster**

Keep its ID on the chip with an unavailable status instead of falling back visually. Compare menu choices to accepted identity so selecting a remaining daemon performs a real bootstrap.

- [x] **Step 2: Cover project writes and overlapping view work**

Route project creation and rename through the shared view-work guard. Reference-count all overlapping work for selector availability while retaining latest-request loading presentation.

- [x] **Step 3: Prove terminal streams close**

Assert a double-failed switch leaves no active connection for the old daemon in addition to starting no replacement stream.

- [x] **Step 4: Run final verification and commit**

Run the full Vite+ suite and complete Chromium/Firefox Kata Playwright spec before committing.

### Task 9: Close remaining transition entry points

**Files:**

- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Test: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

- [x] **Step 1: Guard initial bootstrap and the entire switch surface**

Disable daemon selection during initial bootstrap and make the workspace inert during switching so quick capture, project creation, and other mutation entry points cannot start against provisional state.

- [x] **Step 2: Clear partial rollback state and close its stream**

Fail rollback after its project/list commit but before detail completion, then prove the workspace is cleared again, the connection error remains visible, and the old stream is disconnected.

- [x] **Step 3: Prove removed-daemon operations never fall through**

After removing accepted provenance from the roster, attempt an old-row detail read and mutation before recovery; assert the remaining daemon receives neither request.

- [x] **Step 4: Run final verification and commit**

Run the full Vite+ suite and complete Chromium/Firefox Kata Playwright spec before committing.

### Task 10: Drain live-event refreshes before switching

**Files:**

- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Test: `frontend/src/lib/features/kata/KataWorkspaceEventStream.test.ts`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

- [x] **Step 1: Count stream callbacks as view work**

Keep daemon selection disabled while a live-event callback reloads old-daemon projects or issues, preserving its error propagation to the stream controller.

- [x] **Step 2: Prove switching waits for a stalled stream refresh**

Stall an issue reload triggered by a reset frame, assert the selector remains disabled, then release it and prove a later switch renders only the replacement daemon.

- [x] **Step 3: Prove failed stream refreshes release the gate**

Reject a reset-triggered refresh and assert the selector re-enables, the stream reconnects, and stale rows are not accepted.

- [x] **Step 4: Run final verification and commit**

Run the full Vite+ suite and complete Chromium/Firefox Kata Playwright spec before committing.

### Task 11: Queue route changes until daemon transactions settle

**Files:**

- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

- [x] **Step 1: Defer route effects during switching**

Prevent route pre-effects, view/scope loads, and detail selection from invalidating provisional target or rollback bootstraps. Apply the latest route after `switchingDaemon` clears.

- [x] **Step 2: Cover route churn followed by target failure**

Stall a target bootstrap, navigate browser history to another issue, then fail the target and prove rollback restores accepted provenance before applying the queued route.

- [x] **Step 3: Suppress queued routes after terminal rollback failure**

Queue a project scope while target bootstrap is stalled, fail target and rollback, then prove the workspace stays empty and no scoped request or stream starts.

- [x] **Step 4: Run final verification and commit**

Run the full Vite+ suite and complete Chromium/Firefox Kata Playwright spec before committing.

### Task 12: Bound ordinary Kata proxy requests

**Files:**

- Modify: `internal/server/kata_proxy.go`
- Test: `internal/server/kata_proxy_test.go`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

- [x] **Step 1: Apply a total non-stream request deadline**

Wrap ordinary reverse-proxy requests in a 30-second context deadline that covers headers and body copying for both TCP and Unix transports, while exempting the long-lived event stream.

- [x] **Step 2: Test stalled headers, bodies, and SSE exemption**

Use a short injected deadline to prove connected upstreams are cancelled while a delayed event-stream frame remains deliverable.

- [x] **Step 3: Exercise refresh failure through the real proxy**

Fail a reset-triggered issue refresh in the Playwright backend and assert the selector releases, the stream reconnects, and prior rows remain visible.

- [x] **Step 4: Run final verification and commit**

Run Go proxy tests, the full Vite+ suite, and the complete Chromium/Firefox Kata Playwright spec before committing.

### Task 13: Preserve queued issue routes after successful switches

**Files:**

- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Test: `frontend/tests/e2e-full/kata.spec.ts`

- [x] **Step 1: Avoid publishing a stale bootstrap selection**

Capture the route signature at switch start and publish the bootstrap-selected issue only when the route remains unchanged through the transaction.

- [x] **Step 2: Cover successful switch route churn**

Stall target event synchronization after bootstrap selects its first task, queue a different target task through browser history, then prove the queued detail wins and the target stream starts.

- [x] **Step 3: Run final verification and commit**

Run the full Vite+ suite and complete Chromium/Firefox Kata Playwright spec before committing.
