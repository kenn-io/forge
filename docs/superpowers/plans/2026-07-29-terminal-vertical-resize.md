# Terminal Vertical Resize Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a pooled docked terminal grow vertically with its leaf and propagate the resulting row count through xterm, the websocket, tmux, and the PTY.

**Architecture:** Keep geometry ownership at the terminal leaf. Activate the existing flex-growth contract on `SessionTerminalSlot` by making its immediate leaf body a flex container; leave terminal pooling, resize measurement, resize authority, and keyboard dispatch unchanged. Prove the user-visible boundary with the existing isolated full-stack browser harness and a real `stty size` command.

**Tech Stack:** Svelte 5, xterm.js, Playwright, Vite+, Go-backed isolated workspace server, tmux.

## Global Constraints

- Do not add another Escape or keyboard-dispatch workaround; Chrome already sends `0x1b` through the terminal websocket.
- Keep the change UI-only. Do not change session creation, resize arbitration, provider behavior, or server APIs.
- Use the real tmux-backed Playwright workspace flow for the row-count regression.
- Use Vite+ directly; never use npm.
- Run the repository-local Svelte autofixer before finalizing the component.
- Before committing, run the repository-local `context-sync` skill in `--commit` mode and the mandatory commit skill.

---

### Task 1: Stretch the pooled terminal through the dock leaf

**Files:**

- Modify: `frontend/tests/e2e-full/00-inline-workspace-continuity.spec.ts`
- Modify: `frontend/src/lib/components/terminal/TerminalSplitTree.svelte:594`

**Interfaces:**

- Consumes: `openTerminalPanel(page)`, `readPtyGeometry(page, container, worktreePath, name)`, the `Resize terminal panel` separator, and `SessionTerminalSlot`'s existing `flex: 1 1 auto` contract.
- Produces: a dock leaf whose immediate session slot fills the leaf in both axes; no new functions, props, events, or API types.

- [ ] **Step 1: Add the failing real-browser regression**

Add this test near the start of the existing `inline workspace pane continuity` describe block, after its `beforeEach`:

```ts
test("a dock resize grows both the pooled terminal and its PTY rows", async ({ page }) => {
  test.skip(
    !hasCommand("git") || !hasCommand("tmux", ["-V"]),
    "git and tmux are required for the real workspace flow",
  );
  await page.setViewportSize({ width: 1440, height: 900 });

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api, 10);
    for (let index = 0; index < 2; index += 1) {
      const launch = await api.post(`/api/v1/workspaces/${workspace.id}/runtime/sessions`, {
        data: { target_key: "plain_shell", display_region: "terminal" },
      });
      expect(launch.status(), await launch.text()).toBe(200);
    }

    await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
    const terminal = await openTerminalPanel(page);
    const panel = page.locator(".terminal-panel.open");
    const handle = panel.getByRole("separator", { name: "Resize terminal panel" });
    const leafBody = panel.locator(".terminal-leaf").filter({ has: terminal }).locator(".terminal-leaf-body");

    const beforeHeight = await terminal.evaluate((element) => element.getBoundingClientRect().height);
    const beforePty = await readPtyGeometry(page, terminal, workspace.worktree_path, "dock-resize-before");

    await handle.press("ArrowUp");
    await handle.press("ArrowUp");

    await expect
      .poll(() => terminal.evaluate((element) => element.getBoundingClientRect().height))
      .toBeGreaterThan(beforeHeight + 20);
    const afterLeafHeight = await leafBody.evaluate((element) => element.getBoundingClientRect().height);
    const afterHeight = await terminal.evaluate((element) => element.getBoundingClientRect().height);
    expect(Math.abs(afterLeafHeight - afterHeight)).toBeLessThan(2);

    const afterPty = await readPtyGeometry(page, terminal, workspace.worktree_path, "dock-resize-after");
    expect(afterPty.rows).toBeGreaterThan(beforePty.rows);
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});
```

The production regression this catches is removing the terminal leaf's vertical flex participation: the panel and leaf body grow, but the xterm height and `stty` row count remain unchanged.

- [ ] **Step 2: Run the focused test and verify the expected failure**

Run from `frontend/`:

```bash
../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/00-inline-workspace-continuity.spec.ts --project=chromium --grep "a dock resize grows both the pooled terminal and its PTY rows"
```

Expected: FAIL at the polled terminal-height assertion because `.terminal-leaf-body` grows while `.terminal-container` remains at its previous height; the PTY consequently keeps its previous row count.

- [ ] **Step 3: Apply the minimal leaf-boundary fix**

In `TerminalSplitTree.svelte`, change the existing leaf-body rule to participate in flex layout:

```css
.terminal-leaf-body {
  position: relative;
  display: flex;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}
```

Do not add height declarations to `SessionTerminalSlot`, `PooledSessionTerminal`, or `XtermTerminalPane`; those layers already express the correct growth contract.

- [ ] **Step 4: Run the Svelte analyzer**

Run from the repository root:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/terminal/TerminalSplitTree.svelte
```

Expected: no actionable Svelte errors caused by the change.

- [ ] **Step 5: Re-run the focused browser test**

Run from `frontend/`:

```bash
../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/00-inline-workspace-continuity.spec.ts --project=chromium --grep "a dock resize grows both the pooled terminal and its PTY rows"
```

Expected: PASS. The xterm fills the resized leaf and `stty size` reports more rows.

- [ ] **Step 6: Run the complete affected frontend verification**

Run from the repository root:

```bash
make webui-verify
```

Then run the complete affected Playwright spec from `frontend/`:

```bash
../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/00-inline-workspace-continuity.spec.ts --project=chromium
```

Expected: all checks and tests pass without new warnings.

- [ ] **Step 7: Re-prove the live browser boundary**

In the existing local Chrome page, enlarge and shrink the terminal dock. Confirm that the xterm fills the leaf after both changes, inspect the outgoing websocket resize frame, and run this inside the active shell:

```python
import shutil
shutil.get_terminal_size()
```

Expected: both `columns` and `lines` follow the visible terminal dimensions; `lines` is no longer stuck at six in a tall dock.

- [ ] **Step 8: Commit the implementation**

Run the repository-local context-sync commit workflow, then stage only the implementation and regression:

```bash
git add frontend/src/lib/components/terminal/TerminalSplitTree.svelte frontend/tests/e2e-full/00-inline-workspace-continuity.spec.ts
git commit -m "fix: let docked terminals grow vertically"
```

The commit body should record that pooled terminal slots already carried the correct flex-growth contract, but their block parent prevented vertical geometry from reaching xterm and the PTY.
