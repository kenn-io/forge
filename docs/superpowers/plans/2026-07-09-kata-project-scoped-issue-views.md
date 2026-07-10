# Kata Project-Scoped Issue Views Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure project-scoped Kata views render only rows returned by the selected project's issue-list endpoint.

**Architecture:** Keep unscoped views on the generic issue list and preserve their parallel project/list loading. For scoped views and searches, resolve `activeDaemonId ?? rosterDefaultDaemonId` to a concrete starting daemon, resolve `project_uid` from its project catalog before loading issues, pass the resolved project into the existing `fetchIssuesByStatus` helper, and treat an unknown UID as an empty scope.

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
const daemonId = getDaemonId() ?? getDefaultDaemonId();
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

- Consumes: `getActiveKataDaemon()` and `getDefaultKataDaemon()` from the active-daemon store.
- Produces: operation-wide concrete daemon pinning for `KataTaskAPI.issues()` and `KataTaskAPI.search()`.

- [x] **Step 1: Add failing unit regressions for issue views, searches, and label hydration**

Add `getDefaultDaemonId?: () => string | undefined` to the test-injectable client options. In `taskClient.test.ts`, use fetch wrappers that change the active or default getter after the catalog response and assert the captured headers:

```typescript
expect(issueViewCalls.map((call) => call.headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "home"]);
expect(projectSearchCalls.map((call) => call.headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "home"]);
expect(labelHydrationCalls.map((call) => call.headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "home", "home"]);
```

The label-hydration case must use `query: "rent"`, `label: "money"`, a search response without labels, and `/api/v1/projects/1/issues?status=open` returning the `money` label.

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

Resolve the operation daemon once at both public entry points:

```typescript
const daemonId = opts?.daemonId ?? getDaemonId() ?? getDefaultDaemonId();
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
