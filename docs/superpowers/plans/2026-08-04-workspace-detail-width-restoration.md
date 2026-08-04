# Workspace Detail Width Restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore the Workspaces detail pane to the user's preferred width after temporary layout constraints.

**Architecture:** `WorkspaceTerminalView.svelte` will retain one persisted preferred width and derive a separate rendered width from the current container geometry. Resize input changes the preference; automatic layout reconciliation changes only the rendered projection.

**Tech Stack:** Svelte 5, TypeScript, Vitest Browser Mode

## Global Constraints

- Preserve at least 300 pixels for the terminal whenever the container permits it.
- Preserve the existing 280-pixel detail-pane minimum whenever the container permits it.
- Persist only explicit user resize intent, never an automatic viewport constraint.
- Do not change API, database, provider, or terminal runtime behavior.

---

### Task 1: Separate Preferred and Rendered Workspace Detail Width

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte:462-483,1150-1161,1249-1334,4052-4065`
- Test: `frontend/src/lib/components/terminal/WorkspaceTerminalView.host-visible.browser.svelte.ts`

**Interfaces:**
- Consumes: `containerEl.clientWidth`, `MIN_SIDEBAR_WIDTH`, `MIN_TERMINAL_WIDTH`, `SplitResizeEvent`
- Produces: persisted user-preferred `sidebarWidth` and layout-constrained `renderedSidebarWidth`

- [ ] **Step 1: Write the failing browser regression**

Mount the real `WorkspaceTerminalView` with the right pane open and a 400-pixel
stored preference. Change its available width to force a sub-280-pixel rendered
pane, dispatch the resize path, then restore the available width. Assert the DOM
width returns to 400 pixels and `kenn-forge-workspace-sidebar-width` remains
`"400"` throughout.

- [ ] **Step 2: Run the regression and verify the expected failure**

Run:

```bash
cd frontend && bun run test:browser -- WorkspaceTerminalView.host-visible.browser.svelte.ts
```

Expected: the final rendered width remains at the temporary constrained width,
or local storage contains that constrained value.

- [ ] **Step 3: Implement the minimal state separation**

Derive the rendered width from the preferred width and current maximum. Remove
automatic writes to the preferred width. Make the splitter ARIA value and pane
style consume the rendered width, while pointer and keyboard resizing continue
to update the preferred width using the rendered starting point.

- [ ] **Step 4: Run focused verification**

Run the browser regression again, then run the affected Svelte analyzer and the
repository's focused frontend checks identified by package scripts.

- [ ] **Step 5: Review and publish**

Review the complete diff, run context-sync commit mode and the public-data scrub,
commit with rationale-focused history, push `pr-pane-collapsing`, and open a
pull request against the repository default branch.
