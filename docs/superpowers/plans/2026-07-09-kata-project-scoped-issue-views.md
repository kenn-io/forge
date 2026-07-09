# Kata Project-Scoped Issue Views Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure project-scoped Kata views render only rows returned by the selected project's issue-list endpoint.

**Architecture:** Keep unscoped views on the generic issue list and preserve their parallel project/list loading. For scoped views, resolve `project_uid` from the project catalog before loading issues, pass the resolved project into the existing `fetchIssuesByStatus` helper, and treat an unknown UID as an empty scope.

**Tech Stack:** TypeScript, Vite+ unit tests, Playwright full-stack e2e fixture.

## Global Constraints

- Do not add a compatibility shim or new API route.
- Preserve generic all-project loading for queries without `project_uid`.
- Preserve the bounded `limit=500` query for logbook views.
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
const status = query.view === "logbook" ? "closed" : "open";
const genericIssuesPromise = query.project_uid === undefined ? fetchIssuesByStatus(status) : undefined;
const projectsPromise = fetchProjects();
const issuesPromise =
  genericIssuesPromise ??
  projectsPromise.then((projects) => {
    const project = projects.projects.find((item) => item.uid === query.project_uid);
    return project ? fetchIssuesByStatus(status, undefined, project) : [];
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
