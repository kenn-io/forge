# Light Documentation Terminal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the synthetic Codex terminal match light documentation captures while preserving the current dark capture.

**Approved spec/design:** `docs/superpowers/specs/2026-08-07-light-docs-terminal-design.md`

**Architecture:** Pass the capture's existing `ThemeName` explicitly through the `afterReady` callback to the synthetic transcript renderer. Select one complete docs-only palette before injecting the overlay; do not change app tokens, xterm, or runtime terminal state.

**Tech Stack:** TypeScript, Playwright, Chromium PDF capture, Poppler SVG conversion

## Global Constraints

- The change is limited to the synthetic Codex overlay in documentation captures.
- Light captures use the approved restrained blue-gray palette.
- Dark captures preserve the existing overlay colors.
- Transcript content, typography, spacing, dimensions, asset names, and live xterm behavior do not change.
- Kata issue `p4dy` owns first-class light theming for the live terminal.

---

### Task 1: Theme the synthetic Codex overlay

**Files:**

- Modify: `docs/screenshots/docs-screenshots.spec.ts`
- Modify: `docs/screenshots/README.md`

**Interfaces:**

- Consumes: `CaptureCase.theme: ThemeName`
- Produces: `embedSyntheticCodexTranscript(workspace: Locator, theme: ThemeName): Promise<void>`
- Produces: `afterReady(page: Page, theme: ThemeName): Promise<void>`

- [ ] **Step 1: Add the failing capture assertion**

After the synthetic overlay is installed for `workspace-codex-session` and
`maintainer-overview`, sample the computed overlay and composer background
colors through a one-pixel canvas. Assert that both surfaces are light for a
light capture and dark for a dark capture:

```ts
const [terminalLightness, composerLightness] = await terminal.evaluate((element) => {
  const sample = (target: Element): number => {
    const canvas = document.createElement("canvas");
    canvas.width = 1;
    canvas.height = 1;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("2D canvas context is unavailable");
    context.fillStyle = getComputedStyle(target).backgroundColor;
    context.fillRect(0, 0, 1, 1);
    return Array.from(context.getImageData(0, 0, 1, 1).data.slice(0, 3)).reduce((sum, channel) => sum + channel, 0);
  };
  const prompt = element.querySelector('[aria-label="Codex prompt composer"] > div');
  if (!prompt) throw new Error("Codex prompt surface was not found");
  return [sample(element), sample(prompt)];
});
if (capture.theme === "light") {
  expect(terminalLightness).toBeGreaterThan(650);
  expect(composerLightness).toBeGreaterThan(600);
} else {
  expect(terminalLightness).toBeLessThan(100);
  expect(composerLightness).toBeLessThan(250);
}
```

- [ ] **Step 2: Run a light Codex capture and verify RED**

Run:

```bash
KENN_FORGE_DOCS_SCREENSHOT_DIR=/tmp/kenn-forge-light-terminal-red \
  node node_modules/vite-plus/bin/vp exec -- playwright test \
  --config docs/screenshots/playwright.config.ts --project=chromium \
  --grep "maintainer-overview light"
```

Expected: FAIL because the overlay and composer still use the dark palette.

- [ ] **Step 3: Pass the capture theme into the renderer**

Change the callback and renderer signatures:

```ts
afterReady?: (page: Page, theme: ThemeName) => Promise<void>;
async function embedSyntheticCodexTranscript(workspace: Locator, theme: ThemeName): Promise<void>;
async function showCodexWorkspace(page: Page, theme: ThemeName): Promise<void>;
async function showActivityCodexWorkspace(page: Page, theme: ThemeName): Promise<void>;
```

Invoke the callback with the existing capture theme:

```ts
await capture.afterReady?.(page, capture.theme);
```

- [ ] **Step 4: Select a complete palette before injection**

Add a serializable palette object outside the browser callback. Preserve every
existing dark value and use the approved light roles:

```ts
const syntheticCodexPalettes = {
  light: {
    background: "oklch(97.5% 0.008 250)",
    foreground: "oklch(30% 0.025 255)",
    promptBackground: "oklch(93.5% 0.012 250)",
    promptBorder: "oklch(83% 0.02 250)",
    promptMarker: "oklch(27% 0.025 255)",
    promptText: "oklch(52% 0.025 250)",
    model: "oklch(47% 0.105 80)",
    separator: "oklch(63% 0.02 250)",
    workingDirectory: "oklch(48% 0.085 150)",
  },
  dark: {
    background: "#0d1117",
    foreground: "#c9d1d9",
    promptBackground: "#343941",
    promptBorder: "#444b55",
    promptMarker: "#f0f3f6",
    promptText: "#9da4ad",
    model: "#f6e2b7",
    separator: "#6e7681",
    workingDirectory: "#abdfa7",
  },
} as const;
```

Pass `{ transcript, palette: syntheticCodexPalettes[theme] }` to
`terminal.evaluate` and replace only the hard-coded color declarations with
palette fields.

- [ ] **Step 5: Update the screenshot guide**

State that the synthetic Codex overlay follows the capture theme, while live
terminal theming remains outside the documentation generator.

- [ ] **Step 6: Run focused light and dark captures and verify GREEN**

Run:

```bash
KENN_FORGE_DOCS_SCREENSHOT_DIR=/tmp/kenn-forge-light-terminal-green \
  node node_modules/vite-plus/bin/vp exec -- playwright test \
  --config docs/screenshots/playwright.config.ts --project=chromium \
  --grep "maintainer-overview (light|dark)"
```

Expected: two passing captures; the light output uses the light palette and the
dark output retains the existing dark palette.

- [ ] **Step 7: Run the complete affected verification**

Run:

```bash
make docs-build
node --test scripts/build-docs.test.mjs scripts/docs-screenshots-command.test.mjs
make lint-check
git diff --check
```

Expected: all documentation captures and rendered-site tests pass, including
the iPhone-sized WebKit paint comparison; script tests, lint, and diff checks
also pass.

- [ ] **Step 8: Commit**

Run the repository `context-sync --commit` and mandatory commit workflows,
then commit only the overlay, test, and screenshot-guide changes with a
rationale-focused conventional commit message.
