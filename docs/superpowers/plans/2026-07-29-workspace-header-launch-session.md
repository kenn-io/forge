# Workspace Header Launch Session Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a one-click Launch session shortcut beside Delete workspace in an embedded workspace pane header.

**Approved spec/design:** `docs/superpowers/specs/2026-07-29-workspace-header-launch-session-design.md`

**Architecture:** Reuse the existing `workspaceStripActions` snippet that publishes workspace-level actions into `WorkspacePaneControls`. Add a Play icon button that calls the existing `openLauncher` function, while retaining the popover action for promoted-session leaves that suppress strip actions.

**Tech Stack:** Svelte 5, TypeScript, Lucide Svelte icons, kit-ui `IconButton`, Vitest with Testing Library, Playwright.

## Global Constraints

- The header shortcut is icon-only, appears immediately before Delete workspace, and has the accessible name and tooltip `Launch session`.
- The shortcut is visible only under the same ready-workspace and strip-ownership conditions as Delete workspace.
- The shortcut is disabled whenever other workspace actions are blocked.
- Clicking the shortcut opens the existing `Launch a session` overlay; it does not launch a default target or add an API path.
- Keep the existing Launch session action in the workspace controls popover for panes that omit strip actions.
- Do not add dependencies or compatibility paths.

---

### Task 1: Add the workspace header launch shortcut

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Modify: `docs/superpowers/plans/2026-07-29-workspace-header-launch-session.md` (check off completed steps)

**Interfaces:**
- Consumes: `openLauncher(): void`, the existing `PlayIcon` import, `actionsBlocked`, and `workspaceStripActions()` in `WorkspaceTerminalView.svelte`.
- Produces: A header `IconButton` with `ariaLabel="Launch session"`, `title="Launch session"`, and `onclick={openLauncher}` before the existing Delete workspace button.

- [x] **Step 1: Write the failing component regression test**

  In the existing embedded workspace controls test area of `WorkspaceTerminalView.test.ts`, add focused behavior tests. The production mutations they catch are removal of the direct strip action while the popover action remains available, omission of the `actionsBlocked` binding, and rendering the strip action for a non-ready workspace.

  ```ts
  it("opens the session launcher directly from the workspace pane header", async () => {
    claimForPrs();

    render(WorkspaceTerminalView, {
      props: {
        workspaceId: "ws-1",
        paneSurface: "prs" as const,
      },
    });

    await waitFor(() => expect(hostedWorkspaceControls()).not.toBeNull());
    render(WorkspacePaneControls);

    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();
    const launch = await screen.findByRole("button", { name: "Launch session" });
    const deleteWorkspace = screen.getByRole("button", { name: /^Delete workspace / });
    expect(launch.getAttribute("title")).toBe("Launch session");
    expect(launch.compareDocumentPosition(deleteWorkspace) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);

    await fireEvent.click(launch);
    expect(await screen.findByRole("dialog", { name: "Launch a session" })).toBeTruthy();
  });
  ```

  Add a pending-delete test that keeps the current ready workspace rendered and verifies the live header shortcut becomes disabled while the delete request is unresolved:

  ```ts
  it("disables the header session launcher while workspace deletion is pending", async () => {
    const deleteRequest = deferred<Response>();
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: Request | URL | string, init?: RequestInit) => {
        const method = init?.method ?? (input instanceof Request ? input.method : "GET");
        const { pathname } = new URL(input instanceof Request ? input.url : String(input), "http://localhost");
        if (method === "DELETE" && pathname.endsWith("/workspaces/ws-1")) return deleteRequest.promise;
        if (pathname.endsWith("/workspaces/ws-1")) {
          return Promise.resolve(Response.json(workspaceResponse));
        }
        if (pathname.endsWith("/api/v1/workspaces")) {
          return Promise.resolve(Response.json({ workspaces: [workspaceResponse] }));
        }
        return Promise.resolve(Response.json({}));
      }),
    );
    claimForPrs();

    render(WorkspaceTerminalView, {
      props: { workspaceId: "ws-1", paneSurface: "prs" as const },
    });
    await waitFor(() => expect(hostedWorkspaceControls()).not.toBeNull());
    render(WorkspacePaneControls);
    const launch = await screen.findByRole("button", { name: "Launch session" });
    expect(launch.hasAttribute("disabled")).toBe(false);

    await clickDeleteAndConfirm(screen.getByRole("button", { name: /^Delete workspace / }));
    await waitFor(() => expect(launch.hasAttribute("disabled")).toBe(true));
  });
  ```

  Rename the broken-workspace test to `leaves workspace strip actions off a broken workspace, whose error panel owns delete` and assert `Launch session` is absent too. Also mount `WorkspacePaneControls` in `stays away while a workspace is still being created` and assert the shortcut is absent there.

- [x] **Step 2: Run the focused test and verify RED**

  Run from `frontend/`:

  ```bash
  ../node_modules/.bin/vp test run --project unit WorkspaceTerminalView
  ```

  Expected: FAIL because no `Launch session` button is rendered until the workspace controls popover is opened.

- [x] **Step 3: Add the minimal strip action**

  In `workspaceStripActions()`, immediately before the existing danger-tone Delete workspace `IconButton`, add:

  ```svelte
  <IconButton
    size="sm"
    tone="neutral"
    disabled={actionsBlocked}
    ariaLabel="Launch session"
    title="Launch session"
    onclick={openLauncher}
  >
    <PlayIcon size="13" strokeWidth="2" aria-hidden="true" />
  </IconButton>
  ```

  Update the adjacent comment so it describes both direct workspace actions and explains why Launch remains duplicated in the popover for panes that set `showStripActions={false}`.

- [x] **Step 4: Run focused checks and verify GREEN**

  Run from the repository root:

  ```bash
  vp exec -- svelte-mcp svelte-autofixer ./frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte
  ```

  Run from `frontend/`:

  ```bash
  ../node_modules/.bin/vp test run --project unit WorkspaceTerminalView
  ```

  Expected: Svelte analysis reports no actionable issue in the change and the WorkspaceTerminalView unit suite passes.

- [x] **Step 5: Run the full affected frontend verification**

  Run the full configured frontend unit suite from `frontend/`:

  ```bash
  ../node_modules/.bin/vp test run --project unit
  ```

  Then run the repository frontend check from the repository root:

  ```bash
  ./node_modules/.bin/vp run frontend-package-check
  ```

  Then run the affected Playwright suite from `frontend/`:

  ```bash
  ../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/00-workspace-launcher.spec.ts --project=chromium
  ```

  Expected: all frontend tests and checks pass, and the Chromium workspace launcher suite passes.

- [x] **Step 6: Review and commit the implementation**

  Inspect `git diff`, confirm only the plan, component test, and component implementation changed, run the repository-local `context-sync` skill with `--commit`, then use the mandatory commit skill without amending or bypassing hooks:

  ```bash
  git add docs/superpowers/plans/2026-07-29-workspace-header-launch-session.md \
    frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts \
    frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte
  git commit -m "feat: launch sessions from the workspace header" \
    -m "Starting another workspace session currently requires opening the controls popover first. Keep the popover entry for promoted session panes while giving the workspace pane a direct shortcut beside Delete."
  ```
