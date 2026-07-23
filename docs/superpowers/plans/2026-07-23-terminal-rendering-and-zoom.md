# Terminal Rendering and Zoom Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate xterm WebGL glyph-atlas corruption and add a shared, persisted terminal-only zoom control.

**Architecture:** Preserve the existing xterm and ghostty-web renderers, but apply the already-merged upstream xterm WebGL texture-filter fix as a Bun dependency patch. Put zoom state transitions and serialized persistence in a focused terminal zoom controller consumed by compact terminal chrome and terminal-focused keyboard handling.

**Tech Stack:** Svelte 5, TypeScript, Vitest, Playwright, Bun patched dependencies, xterm.js 6.0.0.

## Global Constraints

- Terminal font size remains an integer from 8 through 32 px.
- Reset restores the existing 14 px default.
- Zoom updates every mounted xterm and ghostty-web pane through the shared settings store.
- Keyboard shortcuts intercept browser zoom only while focus is inside a terminal.
- Persistence uses the existing terminal settings API and rolls back on failure.
- Do not migrate or alias persisted renderer settings.

---

### Task 1: Backport the upstream WebGL atlas fix

**Files:**
- Create: `patches/@xterm+addon-webgl@0.19.0.patch`
- Modify: `package.json`
- Modify: `bun.lock`

**Interfaces:**
- Consumes: Bun's root-level `patchedDependencies` support.
- Produces: An installed `@xterm/addon-webgl` whose atlas upload uses `LINEAR` filters and no mipmap generation.

- [ ] **Step 1: Prepare the dependency for patching**

Run:

```bash
bun patch @xterm/addon-webgl@0.19.0
```

Expected: Bun prepares an unlinked editable package under `node_modules`.

- [ ] **Step 2: Apply the upstream texture upload change**

In `node_modules/@xterm/addon-webgl/src/GlyphRenderer.ts`, replace the mipmap call in `_bindAtlasPageTexture` with:

```ts
gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
```

Apply the equivalent replacement to the distributed `lib/addon-webgl.js` and
`lib/addon-webgl.mjs` files because Vite consumes the package distribution.

- [ ] **Step 3: Commit the Bun dependency patch metadata**

Run:

```bash
bun patch --commit @xterm/addon-webgl@0.19.0
```

Expected: Bun creates `patches/@xterm+addon-webgl@0.19.0.patch` and updates
root `package.json` plus `bun.lock`.

- [ ] **Step 4: Verify reproducible application**

Run:

```bash
bun install --frozen-lockfile --ignore-scripts
rg -n "generateMipmap|TEXTURE_MIN_FILTER|TEXTURE_MAG_FILTER" frontend/node_modules/@xterm/addon-webgl/src/GlyphRenderer.ts
```

Expected: install succeeds; the source contains both linear filters and no
`generateMipmap`.

### Task 2: Build a serialized terminal zoom controller

**Files:**
- Create: `frontend/src/lib/components/terminal/terminalZoom.ts`
- Create: `frontend/src/lib/components/terminal/terminalZoom.test.ts`

**Interfaces:**
- Consumes: `SettingsStore`, `updateSettings`, `flashStore`.
- Produces: `createTerminalZoomController(options)` with `decrease()`,
  `increase()`, `reset()`, `setFontSize(size)`, and `handleKeydown(event)`.

- [ ] **Step 1: Write failing controller tests**

Cover these literal cases:

```ts
expect(clampTerminalFontSize(7)).toBe(8);
expect(clampTerminalFontSize(33)).toBe(32);
expect(clampTerminalFontSize(14.8)).toBe(15);
```

Then assert increase/decrease/reset update a shared fake settings store
immediately, persist the complete terminal object in call order, and restore
the last confirmed settings while flashing an error after a rejected save.
Assert `handleKeydown` claims `Cmd/Ctrl` plus `+`, `-`, or `0` only when
`event.target` is inside `.terminal-container`.

- [ ] **Step 2: Run the focused test and confirm failure**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test src/lib/components/terminal/terminalZoom.test.ts
```

Expected: FAIL because `terminalZoom.ts` does not exist.

- [ ] **Step 3: Implement the controller**

Implement constants and clamping:

```ts
export const TERMINAL_FONT_SIZE_MIN = 8;
export const TERMINAL_FONT_SIZE_MAX = 32;
export const TERMINAL_FONT_SIZE_DEFAULT = 14;

export function clampTerminalFontSize(value: number): number {
  return Math.min(
    TERMINAL_FONT_SIZE_MAX,
    Math.max(TERMINAL_FONT_SIZE_MIN, Math.round(value)),
  );
}
```

Serialize saves through one promise chain. Apply each requested size to the
store synchronously; persist the latest complete terminal settings; on failure
restore the last server-confirmed settings only when no newer request is
pending, then call the supplied error reporter.

- [ ] **Step 4: Run the focused test and confirm success**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test src/lib/components/terminal/terminalZoom.test.ts
```

Expected: PASS.

### Task 3: Add compact zoom controls and focused shortcuts

**Files:**
- Create: `frontend/src/lib/components/terminal/TerminalZoomControl.svelte`
- Create: `frontend/src/lib/components/terminal/TerminalZoomControl.test.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`

**Interfaces:**
- Consumes: the Task 2 controller and shared settings store.
- Produces: accessible `Decrease terminal font size`, `Reset terminal font size`,
  and `Increase terminal font size` actions plus terminal-focused shortcuts.

- [ ] **Step 1: Write failing component tests**

Render `TerminalZoomControl` with a fake controller and assert:

```ts
expect(screen.getByText("14px")).toBeInTheDocument();
await fireEvent.click(screen.getByRole("button", { name: "Decrease terminal font size" }));
expect(decrease).toHaveBeenCalledOnce();
await fireEvent.click(screen.getByRole("button", { name: "Reset terminal font size" }));
expect(reset).toHaveBeenCalledOnce();
await fireEvent.click(screen.getByRole("button", { name: "Increase terminal font size" }));
expect(increase).toHaveBeenCalledOnce();
```

Extend `WorkspaceTerminalView.test.ts` to dispatch focused and unfocused
keyboard events and assert only terminal-focused `Cmd/Ctrl` `+`, `-`, and `0`
invoke the controller and prevent the browser default.

- [ ] **Step 2: Run the component tests and confirm failure**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test src/lib/components/terminal/TerminalZoomControl.test.ts src/lib/components/terminal/WorkspaceTerminalView.test.ts
```

Expected: FAIL because the control and shortcut integration do not exist.

- [ ] **Step 3: Implement the compact control**

Create a dense four-part control using existing border, surface, hover, text,
and spacing tokens. Render minus and plus icons, a `fontSize + "px"` reset
button, and disable the edge actions at 8 and 32.

Instantiate one controller in `WorkspaceTerminalView.svelte`, render the
control directly beside `TerminalOptionsMenu`, and register a window keydown
listener for the lifetime of the visible workspace host. Delegate matching to
the controller so inline and regular placement share identical behavior.

- [ ] **Step 4: Run Svelte analysis and focused tests**

Run the repository Svelte autofixer for both changed `.svelte` files, then:

```bash
cd frontend && ../node_modules/.bin/vp test src/lib/components/terminal/TerminalZoomControl.test.ts src/lib/components/terminal/WorkspaceTerminalView.test.ts
```

Expected: no Svelte errors and both test files pass.

### Task 4: Prove persistence and shared rendering behavior

**Files:**
- Modify: `frontend/tests/e2e-full/00-settings-terminal-font.spec.ts`
- Modify: `frontend/tests/e2e-full/00-inline-workspace-continuity.spec.ts`

**Interfaces:**
- Consumes: the Task 3 UI and existing isolated workspace server.
- Produces: browser-level proof that zoom persists and follows the shared host.

- [ ] **Step 1: Write failing browser assertions**

In the settings terminal font spec, open a workspace terminal, click increase,
and poll `/api/v1/settings` until `terminal.font_size` is 15. Reload and assert
the control still reads `15px`.

In the inline continuity spec, tag the regular workspace terminal, trigger
terminal-focused zoom, move the same host into the inline dock, and assert the
same tagged terminal remains mounted while the control and computed terminal
font size show the persisted value.

- [ ] **Step 2: Run the affected Playwright files**

Run:

```bash
cd frontend && node node_modules/.bin/playwright test --config=playwright-e2e.config.ts --project=chromium tests/e2e-full/00-settings-terminal-font.spec.ts tests/e2e-full/00-inline-workspace-continuity.spec.ts
```

Expected: PASS after Tasks 2 and 3 are complete.

- [ ] **Step 3: Run the full affected frontend suite**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test
cd frontend && ../node_modules/.bin/vp run check
```

Expected: the full Vitest suite and frontend checks pass.

- [ ] **Step 4: Review final scope**

Run:

```bash
git diff --check
git status --short
```

Expected: only the approved dependency patch, terminal zoom implementation,
tests, and design/plan documents are present.
