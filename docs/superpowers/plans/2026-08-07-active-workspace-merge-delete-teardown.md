# Active Workspace Merge-Delete Teardown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return the Workspaces page to its list after merge cleanup deletes the workspace named by the active terminal route, without leaving that dead route in browser history.

**Architecture:** Keep route correction in the central `notifyWorkspaceDeleted` lifecycle handler. After the handler tombstones the deleted workspace and forgets its restorable terminal route, it replaces the URL only when the current terminal route identifies that exact local or Fleet workspace. Protect the whole MergeModal-to-router path with an isolated full-stack browser test that creates and deletes a real SQLite-backed workspace.

**Tech Stack:** TypeScript, Svelte 5 rune stores, Vitest via Vite+

## Global Constraints

- Preserve the current route when a different workspace is deleted.
- Preserve host-key identity so local and Fleet workspaces with the same ID remain distinct.
- Do not change merge, deletion, or API contracts.
- Browser Back must skip terminal routes whose workspace was deleted.

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

---

### Task 2: Review follow-ups for browser history and full-stack coverage

**Files:**
- Modify: `frontend/src/lib/stores/workspace-host.svelte.ts:19,480`
- Test: `frontend/src/lib/stores/workspace-host.test.ts:481-505`
- Test: `frontend/tests/e2e-full/detail-action-buttons.spec.ts:136-202`

**Interfaces:**
- Consumes: `replaceUrl(path: string): void`, the existing `notifyWorkspaceDeleted(...)` callback, and `startIsolatedWorkspaceE2EServer()`
- Produces: replacement navigation for a deleted active terminal route and real API/SQLite browser coverage of merge-triggered cleanup

- [x] **Step 1: Add a failing unit assertion for replacement navigation**

Add this assertion to the active-route test before production code changes:

```ts
navigate("/terminal/ws-a");
const historyLength = history.length;

notifyWorkspaceDeleted("ws-a");

expect(history.length).toBe(historyLength);
expect(desiredKey()).toEqual({ workspaceId: "", hostKey: undefined });
```

The current `navigate()` implementation adds one history entry, so this assertion proves the medium-severity bug.

- [x] **Step 2: Add a failing real API/SQLite Playwright regression**

Extend `detail-action-buttons.spec.ts` with this isolated workflow:

```ts
test("merge cleanup deletes the active workspace and replaces its terminal history entry", async ({ page }) => {
  test.skip(
    !hasCommand("git") || !hasCommand("tmux", ["-V"]),
    "git and tmux are required for the real workspace flow",
  );

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });

    await page.goto(`${isolatedServer.info.base_url}/pulls/github/acme/widgets/1`);
    await expect(page.locator(".pull-detail")).toBeVisible();
    const createResponsePromise = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      response.url() === `${isolatedServer!.info.base_url}/api/v1/workspaces`
    );
    await page.getByRole("button", { name: "Create Workspace", exact: true }).filter({ visible: true }).click();
    const createResponse = await createResponsePromise;
    expect(createResponse.status()).toBe(202);
    const createdWorkspace = (await createResponse.json()) as WorkspaceStatusResponse;
    await waitForWorkspaceReady(api, createdWorkspace.id);

    const launcher = page.getByRole("dialog", { name: "Launch a session" });
    await expect(launcher).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Open in Workspaces" }).click();
    await expect(page).toHaveURL(new RegExp(`/terminal/${createdWorkspace.id}$`));

    await page.locator(".terminal-view .panel-toggle-btn", { hasText: "PR" }).click();
    const sidebar = page.locator(".right-sidebar");
    await expect(sidebar.locator(".pull-detail")).toBeVisible();
    await sidebar.locator(".btn--merge").first().click();
    const modal = page.getByRole("dialog", { name: "Merge Pull Request" });
    await expect(modal.getByRole("checkbox", { name: "Delete workspace after merge" })).toBeChecked();

    const mergeResponse = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname === "/api/v1/pulls/github/acme/widgets/1/merge"
    );
    await modal.getByRole("button", { name: "Merge Anyway" }).click();
    expect((await mergeResponse).status()).toBe(200);

    await expect(page).toHaveURL(/\/workspaces$/);
    expect((await api.get(`/api/v1/workspaces/${createdWorkspace.id}`)).status()).toBe(404);
    await page.goBack();
    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1$/);
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});
```

The test must drive the rendered merge control, observe the real merge response, verify the workspace row is absent through the real API/SQLite boundary, and verify Back skips the deleted terminal route.

- [x] **Step 3: Verify RED in both test layers**

Run from `frontend/`:

```sh
node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/stores/workspace-host.test.ts
node ../node_modules/.bin/playwright test --config=playwright-e2e.config.ts detail-action-buttons.spec.ts --project=chromium --grep "merge cleanup"
```

Expected: the unit test reports an added history entry, and the browser test returns to the deleted terminal route after Back.

- [x] **Step 4: Replace the deleted route instead of pushing another entry**

Import `replaceUrl` from `router.svelte.ts` and change only the exact-match branch:

```ts
replaceUrl("/workspaces");
```

- [x] **Step 5: Verify GREEN and the affected suites**

Run the focused unit test, focused Chromium full-stack test, the complete `detail-action-buttons.spec.ts` Chromium suite, the full frontend unit suite, and `frontend-package-check`. Run the Svelte analyzer on the changed store before finalizing.

- [x] **Step 6: Review for follow-up publication**

Run context sync and the public-data scrub, then prepare one rationale-first follow-up commit for the requested push:

```sh
git add context/ui-interaction-contracts.md \
  frontend/src/lib/stores/workspace-host.svelte.ts \
  frontend/src/lib/stores/workspace-host.test.ts \
  frontend/tests/e2e-full/detail-action-buttons.spec.ts \
  docs/superpowers/plans/2026-08-07-active-workspace-merge-delete-teardown.md
git commit -m "test: cover merged workspace teardown end to end"
git push
```

Report the verified history and full-stack resolution with the pushed follow-up.
