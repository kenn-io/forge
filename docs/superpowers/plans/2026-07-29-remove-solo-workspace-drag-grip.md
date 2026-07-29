# Remove the Solo Workspace Drag Grip Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the detached drag marker from the strip-less solo Workspace pane while preserving its other controls and normal multi-tab dragging.

**Approved spec/design:** `docs/superpowers/specs/2026-07-29-remove-solo-workspace-drag-grip-design.md`

**Architecture:** Keep `TabbedPanelTree`'s existing solo-chrome layout and pane action cluster, but stop creating a dedicated drag source when the outer tab strip is suppressed. Ordinary tab buttons retain the existing `startTabDrag` wiring whenever the strip is rendered.

**Tech Stack:** Svelte 5, TypeScript, Vitest, Testing Library, Vite+

## Global Constraints

- Do not replace the grip with an invisible drag target or a draggable toolbar surface.
- Preserve the hide, caller-provided leaf extras, and maximize controls.
- Preserve ordinary draggable tab buttons in multi-tab leaves.
- Use Vite+ commands, never npm.

---

### Task 1: Remove the Solo Workspace Drag Source

**Files:**
- Modify: `packages/ui/src/components/shared/DetailPaneLayout.test.ts`
- Modify: `packages/ui/src/components/shared/TabbedPanelTree.svelte`

**Interfaces:**
- Consumes: `soloChromeTabKeys`, `tabActions`, and `leafActions` props already exposed by `TabbedPanelTree.svelte`.
- Produces: A solo action cluster containing only `tabActions` and `leafActions`; ordinary rendered tabs continue to use `draggable={tabDragEnabled()}` and `startTabDrag(event, tab)`.

- [x] **Step 1: Write the failing regression assertions**

In the existing `drops the strip for a leaf that holds only the workspace, keeping its controls` test, replace the positive `Move Workspace` assertion with an absence assertion and keep the neighboring control assertions:

```ts
expect(within(cluster).queryByRole("button", { name: "Move Workspace" })).toBeNull();
expect(within(cluster).getByTestId("pane-hide-workspace")).toBeTruthy();
expect(within(cluster).getByTestId("pane-toggle-zoom")).toBeTruthy();
expect(within(cluster).getByTestId("leaf-extra-leaf-workspace")).toBeTruthy();
```

In the existing `brings the strip back when a second tab lands in the workspace leaf` test, assert that the returned Workspace tab remains draggable:

```ts
const workspaceTab = screen.getByRole("tab", { name: "Workspace" });
expect(workspaceTab.getAttribute("draggable")).toBe("true");
```

- [x] **Step 2: Run the focused test to verify the new absence assertion fails**

Run:

```bash
cd frontend
../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/shared/DetailPaneLayout.test.ts
```

Expected: FAIL because the current solo action cluster still contains the accessible `Move Workspace` button. The existing multi-tab draggable assertion should already pass.

- [x] **Step 3: Remove only the solo grip implementation**

In `TabbedPanelTree.svelte`:

- delete the `GripVerticalIcon` import;
- remove the `tabbed-panel-solo-grip` button from `tabbed-panel-solo-actions`;
- update the nearby comment so it describes the remaining actions without claiming a replacement drag source; and
- delete the now-unused `.tabbed-panel-solo-grip` style.

The solo cluster should reduce to:

```svelte
<div class="tabbed-panel-solo-actions" data-testid="tabbed-panel-solo-actions">
  {@render tabActions?.(soloTab)}
  {@render leafActions?.(node)}
</div>
```

Do not change the ordinary tab button's existing drag attributes or handlers.

- [x] **Step 4: Validate the edited Svelte component**

Run:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/shared/TabbedPanelTree.svelte
```

Expected: no Svelte errors or actionable warnings caused by the edit.

- [x] **Step 5: Run the focused component tests**

Run:

```bash
cd frontend
../node_modules/.bin/vp test run --project unit \
  ../packages/ui/src/components/shared/DetailPaneLayout.test.ts \
  ../packages/ui/src/components/shared/TabbedPanelTree.test.ts
```

Expected: PASS.

- [x] **Step 6: Run the full frontend Vitest suite**

Run:

```bash
cd frontend
../node_modules/.bin/vp test
```

Expected: PASS.

- [x] **Step 7: Commit the implementation**

After running the repository `context-sync --commit` and mandatory commit skill workflows, stage only the plan, component, and test:

```bash
git add docs/superpowers/plans/2026-07-29-remove-solo-workspace-drag-grip.md \
  packages/ui/src/components/shared/TabbedPanelTree.svelte \
  packages/ui/src/components/shared/DetailPaneLayout.test.ts
git commit -m "fix: remove the solo workspace drag marker"
```
