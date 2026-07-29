# Terminal-safe Command Palette Chord Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a focused terminal keep ordinary `Ctrl/Meta+K` while reserving `Ctrl/Meta+Shift+K` for opening and closing middleman's command palette.

**Architecture:** Keep terminal ownership as the default at the global dispatcher boundary. Add one exact event-level exception for shifted `K`, then register the same chord on both the global palette action and the active palette modal frame. The real tmux-backed test continues to exercise palette teardown and terminal focus restoration with the safe chord.

**Tech Stack:** TypeScript, Svelte 5, Vite+, Vitest, Playwright, xterm.js, tmux

## Global Constraints

- `Ctrl/Meta+K` remains terminal-owned.
- Only `Ctrl/Meta+Shift+K` bypasses focused-terminal ownership.
- Escape, function keys, `Ctrl/Meta+P`, and every other terminal keystroke remain terminal-owned.
- Use Vite+ directly; never use npm.
- Run the real-terminal Playwright spec in both Chromium and Firefox before pushing.

---

### Task 1: Reserve the shifted palette chord at the dispatcher

**Files:**
- Modify: `frontend/src/lib/stores/keyboard/dispatch.svelte.test.ts:249-301`
- Modify: `frontend/src/lib/stores/keyboard/dispatch.svelte.ts:20-39`

**Interfaces:**
- Consumes: `dispatchKeydown(event: KeyboardEvent, contextProvider: () => Context): void`
- Produces: terminal dispatch behavior where only `{ key: "k", ctrlOrMeta: true, shift: true }` reaches registered app actions.

- [ ] **Step 1: Write the failing dispatcher tests**

Extend the terminal ownership cases so unshifted `Meta+K` still does not dispatch, then add a focused case:

```ts
it("reserves Cmd-Shift-K for the palette while a terminal is focused", () => {
  const palette = register("palette.open", { key: "k", ctrlOrMeta: true, shift: true });
  const { textarea, wrapper } = terminalTargets();

  for (const target of [textarea, wrapper]) {
    const e = event({ key: "k", metaKey: true, shiftKey: true });
    Object.defineProperty(e, "target", { value: target });
    dispatchKeydown(e, () => ctx);
    expect(e.preventDefault).toHaveBeenCalled();
  }

  expect(palette).toHaveBeenCalledTimes(2);
});
```

- [ ] **Step 2: Run the dispatcher test and verify RED**

Run from `frontend/`:

```bash
../node_modules/.bin/vp test src/lib/stores/keyboard/dispatch.svelte.test.ts
```

Expected: FAIL because terminal ownership returns before the shifted palette action can dispatch.

- [ ] **Step 3: Add the exact event-level exception**

Add a focused predicate beside `dispatchKeydown`:

```ts
function isTerminalPaletteShortcut(event: KeyboardEvent): boolean {
  return (
    event.key.toLowerCase() === "k" &&
    (event.ctrlKey || event.metaKey) &&
    event.shiftKey &&
    !event.altKey
  );
}
```

Then narrow the early return:

```ts
if (isTerminalKeyboardTarget(event.target) && !isTerminalPaletteShortcut(event)) return;
```

- [ ] **Step 4: Run the dispatcher test and verify GREEN**

Run the Step 2 command. Expected: PASS, including the existing terminal-ownership cases.

### Task 2: Register and exercise the safe palette chord

**Files:**
- Modify: `frontend/src/lib/stores/keyboard/actions.test.ts:142-150`
- Modify: `frontend/src/lib/stores/keyboard/actions.ts:574-586`
- Modify: `frontend/src/lib/components/keyboard/Palette.svelte.test.ts`
- Modify: `frontend/src/lib/components/keyboard/Palette.svelte:427-440`
- Modify: `frontend/tests/e2e-full/terminal-focus-pane-move.spec.ts:257-266`
- Modify: `context/ui-interaction-contracts.md:212-224`

**Interfaces:**
- Consumes: the dispatcher exception from Task 1 and the existing `togglePalette()` / `closePalette()` handlers.
- Produces: `Ctrl/Meta+Shift+K` in the binding arrays for `palette.open` and `palette.close`.

- [ ] **Step 1: Write failing binding and modal tests**

Update the `palette.open` binding expectation to include the shifted chord first:

```ts
expect(palette!.binding).toEqual([
  { key: "k", ctrlOrMeta: true, shift: true },
  { key: "k", ctrlOrMeta: true },
  { key: "p", ctrlOrMeta: true },
  { key: "p", ctrlOrMeta: true, shift: true },
]);
```

Import `dispatchKeydown` and `Context`, then add a Palette component test that exercises the registered modal action:

```ts
it("closes with Cmd-Shift-K", async () => {
  const { rerender } = render(Palette, { props: {} });
  openPalette();
  await rerender({});
  const input = screen.getByRole("textbox", { name: "Search command palette" });
  const event = new KeyboardEvent("keydown", {
    key: "k",
    metaKey: true,
    shiftKey: true,
    cancelable: true,
  });
  Object.defineProperty(event, "target", { value: input });

  dispatchKeydown(event, () => ({}) as Context);

  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull());
});
```

- [ ] **Step 2: Run the binding/component tests and verify RED**

Run from `frontend/`:

```bash
../node_modules/.bin/vp test src/lib/stores/keyboard/actions.test.ts src/lib/components/keyboard/Palette.svelte.test.ts
```

Expected: FAIL because neither palette binding array contains shifted `K`.

- [ ] **Step 3: Add shifted K to both palette binding arrays**

Insert this binding into `defaultActions` and `modalActions`:

```ts
{ key: "k", ctrlOrMeta: true, shift: true },
```

- [ ] **Step 4: Update the real terminal scenario and durable contract**

Change the pane-move scenario to:

```ts
await page.keyboard.press("Meta+Shift+k");
```

Update the terminal keyboard ownership bullet in `context/ui-interaction-contracts.md` to name `Ctrl/Cmd-Shift-K` as the single documented palette exception while preserving ordinary `Cmd-K` and `Cmd-P` ownership.

- [ ] **Step 5: Run focused tests and Svelte analysis**

Run the Step 2 command, then from the repository root:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/keyboard/Palette.svelte
```

Expected: tests PASS and the autofixer reports no relevant issue.

- [ ] **Step 6: Run the real-terminal spec in both browsers**

Run from `frontend/`:

```bash
../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/terminal-focus-pane-move.spec.ts --project=chromium
../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/terminal-focus-pane-move.spec.ts --project=firefox
```

Expected: the full spec passes in both projects; the pane-move test opens the palette with the shifted chord and sends its post-move marker to the same tmux session without another click.

- [ ] **Step 7: Run the required full frontend verification**

Run from the repository root:

```bash
make frontend-check-no-deps
cd frontend && ../node_modules/.bin/vp test
```

Expected: checks and the complete Vitest suite pass.

- [ ] **Step 8: Commit and push the focused CI fix**

Run the repository-local `context-sync --commit` workflow, inspect the public diff and commit metadata for private data, create a new hook-enforced conventional commit without amending, and push `t3code/fix-tmux-terminal-sizing`.
