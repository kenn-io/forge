# Active Workspace Merge-Delete Teardown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return the Workspaces page to its list after merge cleanup deletes the workspace named by the active terminal route.

**Architecture:** Keep route correction in the central `notifyWorkspaceDeleted` lifecycle handler. After the handler tombstones the deleted workspace and forgets its restorable terminal route, it navigates away only when the current terminal route identifies that exact local or Fleet workspace.

**Tech Stack:** TypeScript, Svelte 5 rune stores, Vitest via Vite+

## Global Constraints

- Preserve the current route when a different workspace is deleted.
- Preserve host-key identity so local and Fleet workspaces with the same ID remain distinct.
- Do not change merge, deletion, or API contracts.

---

### Task 1: Tear down a deleted active Workspaces route

**Files:**
- Modify: `frontend/src/lib/stores/workspace-host.svelte.ts:440-480`
- Test: `frontend/src/lib/stores/workspace-host.test.ts:424-490`

**Interfaces:**
- Consumes: `getRoute(): Route`, `navigate(path: string): void`, and `notifyWorkspaceDeleted(workspaceId: string, hostKey?: string, identity?: WorkspaceItemIdentity): void`
- Produces: the existing deletion callback additionally routes a matching active terminal workspace to `/workspaces`

- [x] **Step 1: Write the failing regression tests**

```ts
it("leaves a deleted active workspace route for the Workspaces list", () => {
  navigate("/terminal/ws-a");
  notifyWorkspaceDeleted("ws-a");
  expect(desiredKey()).toEqual({ workspaceId: "", hostKey: undefined });
});

it("keeps an unrelated active workspace route after deletion", () => {
  navigate("/terminal/ws-b");
  notifyWorkspaceDeleted("ws-a");
  expect(desiredKey()).toEqual({ workspaceId: "ws-b", hostKey: undefined });
});
```

- [x] **Step 2: Run the focused test and verify RED**

Run from `frontend/`:

```sh
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/stores/workspace-host.test.ts
```

Expected: the matching-route test fails because `desiredKey()` still returns `ws-a`; the unrelated-route test passes.

- [x] **Step 3: Implement matching-route navigation**

After forgetting the deleted workspace route, read the current route and navigate only on an exact terminal workspace and host-key match:

```ts
const route = getRoute();
if (
  route.page === "terminal" &&
  route.workspaceId === workspaceId &&
  route.hostKey === hostKey
) {
  navigate("/workspaces");
}
```

- [x] **Step 4: Run focused and frontend checks**

```sh
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/stores/workspace-host.test.ts
node node_modules/vite-plus/bin/vp run frontend-package-check
```

Expected: both commands exit successfully with no test, typecheck, lint, or formatting failures.
Pre-existing lint warnings outside the changed lines do not block this check.

- [x] **Step 5: Review for publication**

Review the exact diff, run context sync, and complete the public-data scrub before entering the commit and pull-request workflow.
