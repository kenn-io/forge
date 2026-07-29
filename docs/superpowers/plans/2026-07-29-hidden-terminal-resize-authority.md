# Hidden Terminal Resize Authority Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent measurable but hidden terminal tabs from claiming or exercising PTY resize authority without disrupting resize delivery for painted split leaves.

**Architecture:** Treat the existing `active` prop as painted state, not focus, and combine it with `terminalRegionSize()` at every resize-authority and resize/refresh send boundary. Keep websockets alive while hidden. Preserve the slot-level rule that every leaf in a painted split receives `active=true`.

**Tech Stack:** TypeScript, Svelte 5, Vite+, Vitest, Playwright, xterm.js, WebSocket, tmux

## Global Constraints

- A hidden terminal keeps its websocket and continues receiving output.
- Resize authority requires both `active=true` and valid measured geometry.
- Unfocused leaves in a painted split remain resize-active.
- Parked and hidden terminals emit no resize or refresh dimensions.
- Use Vite+ directly; never use npm.
- Run the real Chromium/tmux vertical-resize scenario before pushing.

---

### Task 1: Gate PTY resize authority without regressing painted terminals

**Files:**
- Modify: `frontend/src/lib/components/terminal/TerminalPane.test.ts:585-619`
- Modify: `frontend/src/lib/components/terminal/XtermTerminalPane.svelte:266-277,353-364,431-475,515-520,700-707`
- Verify: `frontend/src/lib/components/terminal/TerminalSplitTree.test.ts:100-137`
- Modify: `context/ui-interaction-contracts.md:327-339`

**Interfaces:**
- Consumes: reactive `active: boolean`, where true means the terminal's slot is painted, and `terminalRegionSize(): { cols: number; rows: number } | null`.
- Produces: `resizeAuthorityRegionSize(): { cols: number; rows: number } | null`, which returns a size only when both painted state and geometry permit PTY resize ownership.

- [ ] **Step 1: Replace the contradictory inactive-pane test with hidden-tab regressions**

Use a valid `fitDimensions` value while rendering with `active: false` so the test models `visibility:hidden`, not parking:

```ts
it("does not claim resize authority for a hidden but measurable region", async () => {
  render(TerminalPane, { props: { workspaceId: "ws-123", active: false } });

  await waitFor(() => expect(mockSockets).toHaveLength(1));
  expect(mockSockets[0]!.url).toContain("resize_active=0");

  mockSockets[0]!.onopen?.();
  expect(mockSockets[0]!.sent).toContain(JSON.stringify({ type: "resize_active", active: false }));

  mockSockets[0]!.sent = [];
  fitDimensions = { cols: 100, rows: 40 };
  resizeObserverCallbacks[0]!([], {} as ResizeObserver);

  expect(mockSockets[0]!.sent).toHaveLength(0);
});
```

Add the visible-to-hidden transition case:

```ts
it("revokes authority and ignores later measurements when its tab becomes hidden", async () => {
  const { rerender } = render(TerminalPane, {
    props: { workspaceId: "ws-123", active: true },
  });
  await waitFor(() => expect(mockSockets).toHaveLength(1));
  mockSockets[0]!.onopen?.();
  mockSockets[0]!.sent = [];

  await rerender({ workspaceId: "ws-123", active: false });
  expect(mockSockets[0]!.sent).toContain(JSON.stringify({ type: "resize_active", active: false }));

  mockSockets[0]!.sent = [];
  fitDimensions = { cols: 100, rows: 40 };
  resizeObserverCallbacks[0]!([], {} as ResizeObserver);
  expect(mockSockets[0]!.sent).toHaveLength(0);
});
```

Keep a positive measurable-region case with `active: true`, proving a painted terminal still advertises authority and sends the measured resize:

```ts
it("claims resize authority for a painted measurable region", async () => {
  render(TerminalPane, { props: { workspaceId: "ws-123", active: true } });

  await waitFor(() => expect(mockSockets).toHaveLength(1));
  expect(mockSockets[0]!.url).toContain("resize_active=1");
  mockSockets[0]!.onopen?.();

  mockSockets[0]!.sent = [];
  fitDimensions = { cols: 100, rows: 40 };
  resizeObserverCallbacks[0]!([], {} as ResizeObserver);

  expect(resizeFramesOf(mockSockets[0]!)).toEqual([
    JSON.stringify({ type: "resize", cols: 100, rows: 40 }),
  ]);
});
```

- [ ] **Step 2: Run the terminal tests and verify RED**

Run from `frontend/`:

```bash
../node_modules/.bin/vp test src/lib/components/terminal/TerminalPane.test.ts src/lib/components/terminal/TerminalSplitTree.test.ts
```

Expected: the hidden measurable cases fail because the websocket URL advertises `resize_active=1`, the open socket sends `active: true`, and ResizeObserver still emits a resize.

- [ ] **Step 3: Add one painted-and-measurable size helper**

Keep `terminalRegionSize()` as geometry-only and add:

```ts
function resizeAuthorityRegionSize(): { cols: number; rows: number } | null {
  if (!active) return null;
  return terminalRegionSize();
}
```

Use `resizeAuthorityRegionSize()` for the websocket query's `resize_active`, the socket-open authority frame, and the reactive authority frame:

```ts
const resizeActive = resizeAuthorityRegionSize() !== null ? "1" : "0";
sendResizeActive(resizeAuthorityRegionSize() !== null);
```

- [ ] **Step 4: Gate both resize and refresh sends at execution time**

In `resizeVisibleTerminal()`, obtain the size from `resizeAuthorityRegionSize()` before fitting or sending:

```ts
function resizeVisibleTerminal(): void {
  const size = resizeAuthorityRegionSize();
  if (!size || !terminal) return;

  fitAddon?.fit();
  terminal.refresh(0, Math.max(0, size.rows - 1));
  if (size.cols === sentCols && size.rows === sentRows) return;
  if (sendResize(size.cols, size.rows)) {
    sentCols = size.cols;
    sentRows = size.rows;
  }
}
```

In `refreshVisibleTerminal()`, obtain the same size first, then fit, refresh, and send that measured `{ cols, rows }`. This execution-time check prevents a queued animation frame from sending after `active` flips false:

```ts
function refreshVisibleTerminal(): void {
  const size = resizeAuthorityRegionSize();
  if (!size || !terminal) return;

  fitAddon?.fit();
  terminal.refresh(0, Math.max(0, size.rows - 1));
  if (sendRefresh(size.cols, size.rows)) {
    sentCols = size.cols;
    sentRows = size.rows;
  }
}
```

- [ ] **Step 5: Update the durable interaction contract**

Replace the claim that terminal size is never visibility-gated with the narrower invariant: size and authority require painted state plus valid geometry, painted state is never focus, and every visible split leaf receives it.

- [ ] **Step 6: Run focused tests and Svelte analysis**

Run the Step 2 command, then from the repository root:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/terminal/XtermTerminalPane.svelte
```

Expected: focused tests pass, both visible split leaves remain registered as visible, and the autofixer reports no relevant issue.

- [ ] **Step 7: Run the real tmux resize proof**

Run from `frontend/`:

```bash
../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/00-inline-workspace-continuity.spec.ts --project=chromium
```

Expected: the complete spec passes, including the dock-height test that verifies both rendered xterm height and `stty size` rows grow.

- [ ] **Step 8: Run required full frontend verification**

Run from the repository root:

```bash
make frontend-check-no-deps
cd frontend && ../node_modules/.bin/vp test
```

Expected: formatting, lint, Svelte checks, and the complete Vitest suite pass.

- [ ] **Step 9: Commit and push the review fix**

Run `context-sync --commit`, inspect the public diff and commit metadata for private data, create a new hook-enforced conventional commit without amending, and push `t3code/fix-tmux-terminal-sizing`.
