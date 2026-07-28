# Xterm Clipboard Addon Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make xterm the only terminal renderer, replace custom tmux mouse filtering with `@xterm/addon-clipboard`, and keep terminal input unmodified.

**Architecture:** `TerminalPane.svelte` becomes a thin xterm wrapper and `XtermTerminalPane.svelte` owns the official clipboard addon with the other xterm addons. The terminal settings contract drops renderer selection end-to-end; generated API artifacts and fixtures are regenerated or updated to the smaller shape.

**Tech Stack:** Svelte 5, TypeScript, xterm.js 6, `@xterm/addon-clipboard` 0.2, Vite+/Vitest, Playwright, Go/Huma, TOML.

## Global Constraints

- Start from the existing branch at current `origin/main`; do not switch branches.
- Remove Ghostty and renderer selection directly with no alias, shim, fallback, warning, or dual path.
- Use the addon's standard browser clipboard provider; add no OSC parser, native clipboard endpoint, or gesture-authorization layer.
- Preserve the existing multiline and bracketed-paste behavior.
- Use Bun/Vite+ commands only; never invoke npm.
- Run `make api-generate` after changing the Huma/config settings model.

---

### Task 1: Remove renderer selection from the server contract

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/server/settings_test.go`
- Modify: `internal/server/e2etest/settings_test.go`
- Modify: `internal/server/api_test.go`
- Modify: `config.example.toml`
- Modify: `scripts/e2e/fleet/hub.toml`
- Modify: `scripts/e2e/fleet/member.toml`
- Modify: `scripts/e2e/fleet/member-ssh.toml`
- Modify: `cmd/middleman/lock_e2e_test.go`
- Regenerate: `frontend/openapi/openapi.yaml`
- Regenerate: `packages/ui/src/api/generated/schema.ts`
- Regenerate: `internal/apiclient/generated/client.gen.go`

**Interfaces:**
- Consumes: existing `config.Terminal` settings persistence and Huma settings endpoints.
- Produces: `Terminal` without `renderer`; the remaining fields and JSON/TOML names stay unchanged.

- [ ] **Step 1: Remove the field and regenerate the API**

Delete `TerminalRendererXterm`, `TerminalRendererGhostty`, `Terminal.Renderer`, its defaulting/validation, sample configuration, and renderer assertions/fixtures. Run `make api-generate` so OpenAPI and generated Go/TypeScript types lose the field.

- [ ] **Step 2: Verify the server contract**

Run:

```bash
go test ./internal/config ./internal/server ./internal/server/e2etest -shuffle=on
make api-generate
```

Expected: PASS with generated clients and existing settings behavior aligned to the smaller terminal settings shape. Do not add a test that merely asserts deleted renderer code stays absent.

### Task 2: Make xterm the sole terminal and load ClipboardAddon

**Files:**
- Modify: `frontend/package.json`
- Modify: `bun.lock`
- Modify: `frontend/src/lib/components/terminal/TerminalPane.svelte`
- Modify: `frontend/src/lib/components/terminal/TerminalPane.test.ts`
- Modify: `frontend/src/lib/components/terminal/XtermTerminalPane.svelte`
- Modify: `frontend/tests/e2e/workspace-sidebar.spec.ts`
- Delete: `frontend/src/lib/components/terminal/GhosttyTerminalPane.svelte`
- Delete: `frontend/src/lib/components/terminal/GhosttyTerminalPane.test.ts`
- Delete: `frontend/src/lib/components/terminal/tmuxMouseDragFilter.ts`
- Delete: `frontend/src/lib/components/terminal/tmuxMouseDragFilter.test.ts`
- Modify: `frontend/src/lib/components/terminal/terminal-focus.ts`

**Interfaces:**
- Consumes: `ClipboardAddon` implementing xterm's `ITerminalAddon` and the current terminal WebSocket bridge.
- Produces: one xterm terminal path; all `term.onData(data)` strings are encoded and sent unchanged while the socket is open.

- [ ] **Step 1: Install the addon as dependency setup**

Run: `bun add --cwd frontend @xterm/addon-clipboard@^0.2.0`

Expected: `frontend/package.json` and `bun.lock` add the package and remove nothing else.

- [ ] **Step 2: Write failing terminal integration tests**

Mock the addon with a stable object and record `loadAddon` calls:

```ts
const clipboardAddon = { dispose: vi.fn() };

vi.mock("@xterm/addon-clipboard", () => ({
  ClipboardAddon: vi.fn(() => clipboardAddon),
}));
```

Add assertions that the xterm instance loads `clipboardAddon`, and replace the tiny-drag test expectation with the complete input:

```ts
const input = "\x1b[<0;10;5M\x1b[<32;12;5M\x1b[<0;12;5m";
xtermOnDataHandlers[0]!(input);
expect(sentText(mockSockets[0]!, 0)).toBe(input);
```

Also grant Chromium clipboard permission in a workspace-sidebar Playwright test, expose the test's mocked terminal WebSocket, deliver an `ArrayBuffer` containing `\x1b]52;c;${btoa(text)}\x07`, and poll `navigator.clipboard.readText()` for `text`.

- [ ] **Step 3: Run focused tests and verify RED**

Run:

```bash
(cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/components/terminal/TerminalPane.test.ts)
(cd frontend && ../node_modules/.bin/vp exec -- playwright test tests/e2e/workspace-sidebar.spec.ts --config=playwright.config.ts --project=chromium --grep 'OSC 52')
```

Expected: both fail because `ClipboardAddon` is not loaded, the mouse drag is still filtered, and xterm has no OSC 52 handler.

- [ ] **Step 4: Implement the minimal terminal path**

Import and load the addon in `XtermTerminalPane.svelte`:

```ts
import { ClipboardAddon } from "@xterm/addon-clipboard";

const clipboard = new ClipboardAddon();
term.loadAddon(clipboard);
```

Send `data` directly from `term.onData`, remove the filter import/state, reduce `TerminalPane.svelte` to unconditional `XtermTerminalPane`, delete Ghostty/filter files, and remove the Ghostty dependency.

- [ ] **Step 5: Verify focused tests GREEN**

Run: `(cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/components/terminal/TerminalPane.test.ts)`

Expected: PASS with addon loading and unmodified mouse input.

### Task 3: Remove renderer state from the frontend

**Files:**
- Modify: `packages/ui/src/api/types.ts`
- Modify: `packages/ui/src/stores/settings.svelte.ts`
- Modify: `frontend/src/lib/components/settings/TerminalSettings.svelte`
- Modify: `frontend/src/lib/components/settings/TerminalSettings.test.ts`
- Modify: `frontend/src/lib/components/settings/settingsPanels.ts`
- Modify: `frontend/src/lib/components/terminal/terminalSettingsPersistence.test.ts`
- Modify: `frontend/src/App.activity-collapse.browser.svelte.ts`
- Modify: `frontend/src/App.activity-row-link.browser.svelte.ts`
- Modify: `frontend/src/App.activity-thread-runs.browser.svelte.ts`
- Modify: `frontend/src/App.grouping-toggle.browser.svelte.ts`
- Modify: `frontend/src/App.navigation.browser.svelte.ts`
- Modify: `frontend/src/App.repo-sync.browser.svelte.ts`
- Modify: `frontend/src/Provider.test.ts`
- Modify: `frontend/src/lib/api/settings.test.ts`
- Modify: `frontend/src/lib/components/settings/RepoImportModal.test.ts`
- Modify: `frontend/src/lib/components/settings/RepoSettings.test.ts`
- Modify: `frontend/src/lib/components/terminal/TerminalOptionsMenu.test.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalViewEmbed.test.ts`
- Modify: `frontend/src/lib/utils/appStartup.test.ts`
- Modify: `frontend/src/test/mockApiFetch.ts`
- Modify: `frontend/tests/e2e-full/00-workspace-sidebar.spec.ts`
- Modify: `frontend/tests/e2e-full/settings-globs.spec.ts`
- Modify: `frontend/tests/e2e/activity-collapse-compact-label.spec.ts`
- Modify: `frontend/tests/e2e/activity-threaded-columns.spec.ts`
- Modify: `frontend/tests/e2e/default-branch-activity.spec.ts`
- Modify: `frontend/tests/e2e/detail-stale-actions.spec.ts`
- Modify: `frontend/tests/e2e/mobile-activity-repos.spec.ts`
- Modify: `frontend/tests/e2e/stack-status.spec.ts`
- Modify: `frontend/tests/e2e/workspaces.spec.ts`
- Modify: `frontend/tests/e2e-full/00-settings-terminal-font.spec.ts`
- Modify: `frontend/tests/e2e-full/00-inline-workspace-continuity.spec.ts`
- Modify: `frontend/tests/profiling/README.md`
- Modify: `frontend/tests/profiling/workspace-switch.spec.ts`
- Modify: `context/ui-interaction-contracts.md`

**Interfaces:**
- Consumes: regenerated `TerminalSettings` without `renderer` from Task 1.
- Produces: terminal settings UI/store/fixtures with no renderer draft, getter, setter, selector, or renderer-specific disabled controls.

- [ ] **Step 1: Remove renderer UI/state and update fixtures**

Delete `TerminalRenderer`, the default `renderer`, store getter/setter, renderer draft/options/select, renderer comparisons, and Ghostty-only explanatory/disabled state. Keep line height, letter spacing, and ligature controls always enabled. Remove renderer properties from typed settings fixtures and delete renderer-switching e2e steps.

Update the focus invariant in `context/ui-interaction-contracts.md` to describe the sole xterm pane and its asynchronous font-load boundary without Ghostty or live renderer swaps.

- [ ] **Step 2: Run Svelte analysis and focused tests**

Run:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/terminal/TerminalPane.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/terminal/XtermTerminalPane.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/settings/TerminalSettings.svelte
(cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/components/settings/TerminalSettings.test.ts src/lib/components/terminal/TerminalPane.test.ts)
```

Expected: no actionable autofixer findings and PASS.

### Task 4: Verify OSC 52 clipboard integration in a real browser

**Files:**
- Modify: `frontend/tests/e2e/workspace-sidebar.spec.ts`

**Interfaces:**
- Consumes: the existing mocked workspace-terminal WebSocket and Chromium clipboard permission grants.
- Produces: a Playwright regression that sends an OSC 52 write through the rendered xterm parser and observes `navigator.clipboard`.

- [ ] **Step 1: Re-run the test written RED in Task 2 and verify GREEN**

Run: `(cd frontend && ../node_modules/.bin/vp exec -- playwright test tests/e2e/workspace-sidebar.spec.ts --config=playwright.config.ts --project=chromium --grep 'OSC 52')`

Expected: PASS and clipboard text equals `copied through xterm`.

- [ ] **Step 2: Verify the Firefox-family path in Zen with Computer Use**

Start an isolated `cmd/e2e-server` instance with its private temporary database and tmux socket, then use the `computer-use` skill to open its URL in Zen. Create/open the seeded issue workspace terminal, run `tmux set-buffer -w 'copied through zen'`, and paste the browser clipboard back into a non-submitted terminal prompt. Confirm the visible pasted text is exactly `copied through zen`, then stop only the isolated server created for this check.

### Task 5: Final verification and commit

**Files:**
- Verify all files changed by Tasks 1–4.

**Interfaces:**
- Consumes: completed implementation.
- Produces: a verified branch commit with generated artifacts and context synchronized.

- [ ] **Step 1: Run complete affected verification**

Run:

```bash
(cd frontend && ../node_modules/.bin/vp test run --project unit)
(cd frontend && ../node_modules/.bin/vp test run --project browser)
./node_modules/.bin/vp run frontend-package-check
(cd frontend && ../node_modules/.bin/vp exec -- playwright test tests/e2e/workspace-sidebar.spec.ts --config=playwright.config.ts --project=chromium)
(cd frontend && ../node_modules/.bin/vp exec -- playwright test tests/e2e-full/00-settings-terminal-font.spec.ts tests/e2e-full/00-inline-workspace-continuity.spec.ts --config=playwright-e2e.config.ts --project=chromium)
go test ./internal/config ./internal/server ./internal/server/e2etest ./cmd/middleman -shuffle=on
make api-generate
git diff --check
```

Expected: every command passes with no renderer references outside historical reports/specs and no Ghostty/filter implementation files.

- [ ] **Step 2: Run context sync and commit**

Run the repository-local `context-sync --commit` workflow, stage only this work, and commit with a conventional message explaining why the official addon and single renderer replace the custom mouse interception. Do not push.
