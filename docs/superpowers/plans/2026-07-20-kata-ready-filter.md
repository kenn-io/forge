# Kata Ready Task Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `Ready` Kata task-list filter backed by Kata's authoritative global and project-ready endpoints.

**Architecture:** Extend the existing search-filter contract with a list-only `ready` value and route that value through the existing Kata task client. The client reads the daemon's ready endpoint, normalizes ordinary open task summaries, and applies the current project, owner, label, and text filters; existing workspace and list components keep using task status only to determine whether returned rows are presentation-compatible.

**Tech Stack:** Svelte 5, TypeScript, Vite+, Vitest with jsdom, the existing Kata HTTP proxy client.

## Global Constraints

- Readiness is authoritative from the selected Kata daemon; never derive it from `blocked_by`, `blocks`, parent, or child fields.
- Support both `GET /api/v1/ready` and `GET /api/v1/projects/{project_id}/ready` through the existing Kata proxy.
- Keep `Ready` as a filter mode; returned tasks retain their daemon-provided `status`, normally `"open"`.
- Keep the status menu order `Open`, `Ready`, `Closed`, `All`.
- Project, owner, label, and text-query controls continue narrowing the ready result.
- Do not add a Ready sidebar view, a Middleman backend route, or a local-readiness fallback.
- Use `vp`, never npm, and run the Svelte autofixer on every modified `.svelte` file.
- Apply `skills/context-sync/SKILL.md --commit` immediately before each agent-created commit, then use the mandatory commit skill without amending or bypassing hooks.

---

### Task 1: Add daemon-backed Ready searches to the Kata task client

**Files:**
- Create: `frontend/src/lib/api/kata/taskFilters.ts`
- Modify: `frontend/src/lib/api/kata/taskTypes.ts`
- Modify: `frontend/src/lib/api/kata/taskClient.ts`
- Test: `frontend/src/lib/api/kata/taskClient.test.ts`

**Interfaces:**
- Produces: `KataTaskStatusFilter = "open" | "ready" | "closed" | "all"`.
- Produces: `kataTaskStatusMatchesFilter(issue: Pick<KataTaskSummary, "status">, filter: KataTaskStatusFilter): boolean`; `ready` accepts an open task only after an authoritative ready-list read has established membership.
- Preserves: `KataTaskAPI.search(filters, opts): Promise<KataTaskSearchResponse>`; callers need no new API method.

- [ ] **Step 1: Write failing client tests for global and project Ready reads**

Add these focused cases inside `describe("kata task HTTP client", ...)` in `taskClient.test.ts`:

```ts
test("reads global ready tasks from the daemon and narrows them with search controls", async () => {
  const matching = {
    ...issue("issue-ready", "Ship ready filter", "project-work"),
    owner: "agent:planner",
    labels: ["urgent"],
  };
  const wrongOwner = { ...issue("issue-other", "Ship other filter", "project-work"), owner: "agent:other" };
  const { calls, fetchImpl } = createFetchStub({
    "/api/v1/ready": { body: { issues: [matching, wrongOwner] } },
  });
  const api = createKataTaskAPI({ fetchImpl, getDaemonId: () => "work" });

  const results = await api.search({
    scope: { kind: "all" },
    status: "ready",
    owner: "agent:planner",
    label: "urgent",
    query: "ship ready",
  });

  expect(results.issues.map((item) => item.uid)).toEqual(["issue-ready"]);
  expect(proxyPath(calls[0]!.url)).toBe("/api/v1/ready");
  expect(calls[0]!.headers.get(KATA_DAEMON_HEADER)).toBe("work");
});

test("reads project ready tasks with supported daemon filters and applies text search locally", async () => {
  const ready = {
    ...issue("issue-ready", "Ship ready filter", "project-work"),
    owner: "agent:planner",
    labels: ["urgent"],
  };
  const { calls, fetchImpl } = createFetchStub({
    "/api/v1/projects?include=stats": {
      body: { projects: [project("project-work", "Work")] },
    },
    "/api/v1/projects/1/ready?owner=agent%3Aplanner&label=urgent": {
      body: { issues: [ready, issue("issue-no-match", "Refine backlog", "project-work")] },
    },
  });
  const api = createKataTaskAPI({ fetchImpl });

  const results = await api.search({
    scope: { kind: "project", project_uid: "project-work" },
    status: "ready",
    owner: "agent:planner",
    label: "urgent",
    query: "ship ready",
  });

  expect(results.issues.map((item) => item.uid)).toEqual(["issue-ready"]);
  expect(calls.map((call) => proxyPath(call.url))).toEqual([
    "/api/v1/projects?include=stats",
    "/api/v1/projects/1/ready?owner=agent%3Aplanner&label=urgent",
  ]);
});
```

- [ ] **Step 2: Run the client tests and verify the missing contract fails**

Run:

```bash
./node_modules/.bin/vp test run --project unit frontend/src/lib/api/kata/taskClient.test.ts
```

Expected: FAIL because `"ready"` is not assignable to `KataTaskStatusFilter` and no ready endpoint is requested.

- [ ] **Step 3: Extend the filter type and add status-compatibility logic**

Change the filter union in `taskTypes.ts`:

```ts
export type KataTaskStatusFilter = "open" | "ready" | "closed" | "all";
```

Create `taskFilters.ts`:

```ts
import type { KataTaskStatusFilter, KataTaskSummary } from "./taskTypes.js";

export function kataTaskStatusMatchesFilter(
  issue: Pick<KataTaskSummary, "status">,
  filter: KataTaskStatusFilter,
): boolean {
  if (filter === "all") return true;
  return issue.status === (filter === "ready" ? "open" : filter);
}
```

Import this helper into `taskClient.ts` and replace the status line in `filterSearchIssues` with:

```ts
if (!kataTaskStatusMatchesFilter(issue, filters.status)) return false;
```

- [ ] **Step 4: Implement authoritative Ready endpoint reads**

Add this helper next to `fetchIssuesByStatus` in `taskClient.ts`:

```ts
async function fetchReadyIssues(
  filters: KataTaskSearchFilters,
  daemonId?: string,
  project?: KataProjectSummary,
  pinned = false,
  signal?: AbortSignal,
): Promise<KataTaskSummary[]> {
  const params = new URLSearchParams();
  if (project && filters.owner.trim()) params.set("owner", filters.owner.trim());
  if (project && filters.label.trim()) params.append("label", filters.label.trim());
  const suffix = params.size > 0 ? `?${params.toString()}` : "";
  const basePath = project ? `/projects/${project.id}/ready` : "/ready";
  const result = await request<unknown>(taskPath(`${basePath}${suffix}`), {
    headers: pinned ? pinnedDaemonHeaders(daemonId) : daemonHeaders(daemonId),
    signal,
  });
  return normalizeKataTaskList(result.body)
    .groups.flatMap((group) => group.issues)
    .map((issue) => withProjectIdentity(issue, project));
}
```

In `searchAllProjects`, branch before the `all` handling:

```ts
if (filters.status === "ready") {
  return filterSearchIssues(await fetchReadyIssues(filters, daemonId, undefined, pinned, signal), filters);
}
```

In `searchProjectIssueList`, branch before the `all` handling:

```ts
if (filters.status === "ready") {
  return filterSearchIssues(await fetchReadyIssues(filters, daemonId, project, pinned, signal), filters);
}
```

In `searchProject`, ensure a Ready query stays on the ready endpoint and is narrowed locally:

```ts
if (filters.query.trim() === "" || filters.status === "ready") {
  return searchProjectIssueList(filters, project, daemonId, pinned, signal);
}
```

- [ ] **Step 5: Run focused tests and the formatter/linter for the changed TypeScript**

Run:

```bash
./node_modules/.bin/vp test run --project unit frontend/src/lib/api/kata/taskClient.test.ts
./node_modules/.bin/vp lint frontend/src/lib/api/kata/taskClient.ts frontend/src/lib/api/kata/taskFilters.ts frontend/src/lib/api/kata/taskTypes.ts frontend/src/lib/api/kata/taskClient.test.ts
```

Expected: both commands PASS with no warnings or errors.

- [ ] **Step 6: Context-sync and commit the client contract**

Follow `skills/context-sync/SKILL.md --commit`, then stage only the four Task 1 files and create a hook-verified commit:

```bash
git add frontend/src/lib/api/kata/taskFilters.ts frontend/src/lib/api/kata/taskTypes.ts frontend/src/lib/api/kata/taskClient.ts frontend/src/lib/api/kata/taskClient.test.ts
git commit -m "feat: read authoritative Kata ready tasks"
```

The commit body must explain that Kata owns readiness semantics and that local filtering only narrows daemon-selected tasks.

---

### Task 2: Expose, preserve, and render the Ready filter

**Files:**
- Modify: `frontend/src/lib/features/kata/KataSearchPanel.svelte`
- Modify: `frontend/src/lib/features/kata/KataSearchPanel.test.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueList.svelte`
- Modify: `frontend/src/lib/components/kata/KataIssueList.test.ts`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
- Modify: `frontend/src/lib/features/kata/kataWorkspacePersistence.ts`
- Modify: `frontend/src/lib/features/kata/kataWorkspacePersistence.test.ts`

**Interfaces:**
- Consumes: `KataTaskStatusFilter` with `"ready"` and `kataTaskStatusMatchesFilter(...)` from Task 1.
- Preserves: `KataSearchPanel`'s `onChange(filters)` callback and `KataIssueList`'s existing `statusFilter` prop.
- Produces: a status combobox option labelled `Ready`, persisted as `filters.status = "ready"`.

- [ ] **Step 1: Write failing UI and persistence tests**

Add a search-panel test:

```ts
test("emits the Ready status filter", async () => {
  const onChange = vi.fn();
  render(KataSearchPanel, { props: { filters, projects, onChange } });

  await fireEvent.click(screen.getByRole("combobox", { name: "Status: Open" }));
  await fireEvent.click(screen.getByRole("option", { name: "Ready" }));

  expect(onChange).toHaveBeenCalledWith({ ...filters, status: "ready" });
});
```

Add an issue-list test using one open and one closed task in the same view:

```ts
it("renders authoritative ready rows as open tasks", () => {
  const closed = task({
    ...baseIssues[1]!,
    id: 103,
    uid: "issue-closed",
    short_id: "closed",
    qualified_id: "Work#closed",
    title: "Closed task",
    status: "closed",
  });
  render(KataIssueList, {
    props: {
      currentView: { ...currentView, groups: [{ id: "ready", title: "Ready", issues: [baseIssues[0]!, closed] }] },
      selectedIssueUID: null,
      loading: false,
      statusFilter: "ready",
      onSelect: () => {},
    },
  });

  expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /Closed task/ })).toBeNull();
});
```

Add a persistence test:

```ts
it("round-trips the Ready filter", () => {
  const ready = { ...home, filters: { ...home.filters, status: "ready" as const } };

  saveKataWorkspaceState("home", ready);

  expect(loadKataWorkspaceState("home")).toEqual(ready);
});
```

Add a workspace test beside the existing status-filter tests:

```ts
it("loads and keeps an authoritative Ready result selected", async () => {
  const readyIssue = issue("issue-ready", "Ready work", "project-kata");
  const { api, search } = createWorkspaceAPI([readyIssue]);
  search.mockImplementation(async (nextFilters: KataTaskSearchFilters) => ({
    filters: nextFilters,
    issues: nextFilters.status === "ready" ? [readyIssue] : [],
    fetched_at: fetchedAt,
  }));

  render(KataWorkspace, { props: { api, routeScopeUID: "project-kata" } });
  await fireEvent.click(await screen.findByRole("combobox", { name: "Status: Open" }));
  await fireEvent.click(screen.getByRole("option", { name: "Ready" }));

  await waitFor(() =>
    expect(search).toHaveBeenLastCalledWith(
      { scope: { kind: "project", project_uid: "project-kata" }, status: "ready", owner: "", label: "", query: "" },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    ),
  );
  expect(screen.getByRole("button", { name: /Ready work/ })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Ready work" })).toBeTruthy();
});
```

- [ ] **Step 2: Run the focused tests and verify they fail for missing Ready UI/state support**

Run:

```bash
./node_modules/.bin/vp test run --project unit frontend/src/lib/features/kata/KataSearchPanel.test.ts frontend/src/lib/components/kata/KataIssueList.test.ts frontend/src/lib/features/kata/kataWorkspacePersistence.test.ts frontend/src/lib/features/kata/KataWorkspace.test.ts
```

Expected: FAIL because the option is absent, persistence rejects `ready`, and open ready rows are filtered out.

- [ ] **Step 3: Add Ready to the search control and persistence allowlist**

Change `statusOptions` in `KataSearchPanel.svelte` to:

```ts
const statusOptions = [
  { value: "open", label: "Open" },
  { value: "ready", label: "Ready" },
  { value: "closed", label: "Closed" },
  { value: "all", label: "All" },
];
```

Change the persistence allowlist in `kataWorkspacePersistence.ts` to:

```ts
const statusFilters = new Set<KataTaskSearchFilters["status"]>(["open", "ready", "closed", "all"]);
```

- [ ] **Step 4: Use the shared status-compatibility helper in list and workspace flows**

Import `kataTaskStatusMatchesFilter` into `KataIssueList.svelte` and replace both direct status comparisons:

```ts
function issueMatchesStatusFilter(issue: KataTaskSummary): boolean {
  return kataTaskStatusMatchesFilter(issue, statusFilter);
}

function filterGroupsByStatus(
  groups: KataCurrentView["groups"],
  status: KataTaskSearchFilters["status"],
): KataCurrentView["groups"] {
  return groups
    .map((group) => ({
      ...group,
      issues: group.issues.filter((issue) => kataTaskStatusMatchesFilter(issue, status)),
    }))
    .filter((group) => group.issues.length > 0);
}
```

Import the same helper into `KataWorkspace.svelte`. Replace `statusMatches` and the selected-task comparison with:

```ts
function statusMatches(issue: KataTaskSummary, status: KataTaskSearchFilters["status"]): boolean {
  return kataTaskStatusMatchesFilter(issue, status);
}

function selectedIssueMatchesStatusFilter(status: KataTaskSearchFilters["status"]): boolean {
  const selected = store.selectedIssue?.issue;
  return !selected || kataTaskStatusMatchesFilter(selected, status);
}
```

- [ ] **Step 5: Run Svelte analysis and focused tests**

Run from the repository root:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/features/kata/KataSearchPanel.svelte --svelte-version 5
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/kata/KataIssueList.svelte --svelte-version 5
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/features/kata/KataWorkspace.svelte --svelte-version 5
./node_modules/.bin/vp test run --project unit frontend/src/lib/features/kata/KataSearchPanel.test.ts frontend/src/lib/components/kata/KataIssueList.test.ts frontend/src/lib/features/kata/kataWorkspacePersistence.test.ts frontend/src/lib/features/kata/KataWorkspace.test.ts
```

Expected: autofixers report no unresolved Svelte issues and all focused tests PASS.

- [ ] **Step 6: Run the full affected frontend verification**

Run after the final frontend and test edit:

```bash
./node_modules/.bin/vp run frontend-package-check
./node_modules/.bin/vp test run --project unit
git diff --check
```

Expected: all commands PASS. No Playwright run is required because this change does not touch browser geometry, Playwright specs, or shared Playwright fixtures.

- [ ] **Step 7: Context-sync and commit the Ready UI behavior**

Follow `skills/context-sync/SKILL.md --commit`, then stage only the eight Task 2 files and create a hook-verified commit:

```bash
git add frontend/src/lib/features/kata/KataSearchPanel.svelte frontend/src/lib/features/kata/KataSearchPanel.test.ts frontend/src/lib/components/kata/KataIssueList.svelte frontend/src/lib/components/kata/KataIssueList.test.ts frontend/src/lib/features/kata/KataWorkspace.svelte frontend/src/lib/features/kata/KataWorkspace.test.ts frontend/src/lib/features/kata/kataWorkspacePersistence.ts frontend/src/lib/features/kata/kataWorkspacePersistence.test.ts
git commit -m "feat: add Ready to Kata task filters"
```

The commit body must explain that Ready tasks remain ordinary open task records while list membership comes only from Kata's ready response.
