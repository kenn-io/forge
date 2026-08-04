# Generated Workflow Screenshot Lightbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make generated workflow screenshots zoomable and make the first home-page screenshot show Activity with a selected pull request and its live Codex workspace.

**Approved spec/design:** `docs/superpowers/specs/2026-08-03-generated-workflow-screenshot-lightbox-design.md`

**Architecture:** Extend the real seeded Playwright capture so the existing `maintainer-overview` SVGs serialize the selected Activity workflow. Progressively enhance only `figure.workflow-shot` images after Zensical renders them, using a dependency-free native `<dialog>` created by the site override.

**Tech Stack:** TypeScript Playwright, Node.js docs build, Zensical/Material template override, HTML `<dialog>`, CSS

## Global Constraints

- Apply the lightbox only to generated `figure.workflow-shot` screenshots.
- Keep light and dark SVG screenshots generated in the staged docs tree; never track the assets in Git.
- Use the real isolated seeded e2e backend and synthetic Codex target; never use mocked API fixtures, a developer daemon, the installed Codex binary, or agent credentials.
- Use no new browser dependency.
- Preserve image alt text, captions, theme switching, generated asset paths, and the public-docs allowlist.
- Spell the product `kenn-forge` in all documentation prose.
- Use Bun/Vite+ commands; never use npm.

---

### Task 1: Generate the complete Activity workspace composition

**Files:**
- Modify: `docs/screenshots/docs-screenshots.spec.ts`
- Modify: `docs/index.md`
- Modify: `docs/screenshots/README.md`

**Interfaces:**
- Consumes: `ensureSyntheticCodexWorkspace(page: Page, baseURL: string): Promise<void>` and the existing seeded pull request titled `Add widget caching layer`.
- Produces: `showActivityCodexWorkspace(page: Page): Promise<void>`, used as the `afterReady` callback for both `maintainer-overview` capture cases.

- [x] **Step 1: Add failing composition assertions**

In `docs/screenshots/docs-screenshots.spec.ts`, add a callback that clicks the seeded PR row and requires the selected Activity detail, hosted workspace, and selected Codex tab:

```ts
async function showActivityCodexWorkspace(page: Page): Promise<void> {
  const prRow = page
    .locator(".activity-row")
    .filter({ has: page.locator(".badge", { hasText: "PR" }) })
    .filter({ hasText: "Add widget caching layer" })
    .first();
  await prRow.click();

  const detail = page.locator(".activity-detail");
  await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
  await expect(detail.locator(".detail-title")).toContainText("Add widget caching layer");

  const workspace = page.locator(".detail-pane-workspace-slot .workspace-host-wrapper");
  await expect(workspace).toBeVisible();
  const workflow = workspace.getByRole("region", { name: "Workflow panes" });
  const codexTab = workflow.getByRole("tab", { name: "Codex" });
  await expect(codexTab).toBeVisible();
  await codexTab.click();
  await expect(codexTab).toHaveAttribute("aria-selected", "true");
  await expect(workspace.locator(".terminal-view")).toBeVisible();
}
```

Assign only `afterReady: showActivityCodexWorkspace` to the two `maintainer-overview` cases. This intentionally leaves the workspace unseeded so the new real-UI assertions fail.

- [x] **Step 2: Run the light capture and verify RED**

Run:

```bash
mkdir -p tmp/docs-screenshots-red
env KENN_FORGE_DOCS_SCREENSHOT_DIR=tmp/docs-screenshots-red \
  node node_modules/vite-plus/bin/vp exec -- playwright test \
  --config docs/screenshots/playwright.config.ts --project=chromium \
  --grep "maintainer-overview light"
```

Expected: FAIL because `.detail-pane-workspace-slot .workspace-host-wrapper` is absent without a prepared workspace.

- [x] **Step 3: Seed the live workspace before both captures**

Add `prepare: ensureSyntheticCodexWorkspace` to both `maintainer-overview` cases. Keep `afterReady: showActivityCodexWorkspace`, so the capture selects the PR after Activity loads and selects the already-running Codex session before SVG serialization.

Update the two capture descriptions, the home-page alt text, and the home-page caption to describe Activity with the selected PR and live coding-agent workspace. Update `docs/screenshots/README.md` to state that the overview capture composes that seeded flow.

- [x] **Step 4: Run both captures and verify GREEN**

Run:

```bash
mkdir -p tmp/docs-screenshots-green
env KENN_FORGE_DOCS_SCREENSHOT_DIR=tmp/docs-screenshots-green \
  node node_modules/vite-plus/bin/vp exec -- playwright test \
  --config docs/screenshots/playwright.config.ts --project=chromium \
  --grep "maintainer-overview"
```

Expected: 2 passed; both SVGs contain `activity-shell--split`, `Add widget caching layer`, `workspace-host-wrapper`, and a selected Codex workflow tab.

- [x] **Step 5: Inspect the generated SVGs**

Open `tmp/docs-screenshots-green/maintainer-overview-light.svg` and `tmp/docs-screenshots-green/maintainer-overview-dark.svg`. Confirm the Activity rail, PR detail, hosted workspace, and selected Codex session are all visible without clipping or private host paths.

---

### Task 2: Add the generated-screenshot-only lightbox

**Files:**
- Modify: `docs/site/docs-site.spec.ts`
- Modify: `docs/overrides/main.html`
- Modify: `docs/stylesheets/extra.css`

**Interfaces:**
- Consumes: rendered `figure.workflow-shot img.workflow-shot__image` elements and their `src`/`alt` attributes.
- Produces: `.workflow-shot__trigger` buttons and one `dialog.workflow-shot-lightbox` with an image and a `Close expanded screenshot` button.

- [x] **Step 1: Write the failing rendered-site interaction test**

Add a Playwright test that opens `/`, locates the visible generated screenshot trigger by accessible name, and verifies all three close paths plus focus restoration:

```ts
test("opens only the active generated workflow screenshot in a lightbox", async ({ page }) => {
  await page.goto("/");

  const trigger = page.getByRole("button", {
    name: /View kenn-forge Activity.*at full size/i,
  });
  const dialog = page.getByRole("dialog", { name: "Expanded workflow screenshot" });

  await trigger.click();
  await expect(dialog).toBeVisible();
  await expect(dialog.locator("img")).toHaveAttribute("src", /maintainer-overview-light\.svg$/);
  await dialog.getByRole("button", { name: "Close expanded screenshot" }).click();
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await dialog.click({ position: { x: 2, y: 2 } });
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
});
```

Add a dark-system context assertion that the visible trigger opens `maintainer-overview-dark.svg`, not the hidden light SVG.

- [x] **Step 2: Run the new tests and verify RED**

Run:

```bash
env KENN_FORGE_DOCS_SITE_DIR=site \
  node node_modules/vite-plus/bin/vp exec -- playwright test \
  --config docs/site/playwright.config.ts --project=chromium \
  --grep "generated workflow screenshot"
```

Expected: FAIL because no accessible zoom trigger or dialog exists.

- [x] **Step 3: Implement progressive native-dialog enhancement**

In `docs/overrides/main.html`, keep the existing first-frame theme script and add a post-bundle script that:

1. Returns without changing the inline figures unless `HTMLDialogElement.prototype.showModal` exists.
2. Wraps each unenhanced `figure.workflow-shot img.workflow-shot__image` in a real button with class `workflow-shot__trigger`, a theme modifier copied from the image, and `aria-label="View ${alt} at full size"`.
3. Creates one `dialog.workflow-shot-lightbox` with `aria-label="Expanded workflow screenshot"`, an image, and a `Close expanded screenshot` button.
4. Copies only the activated image's `src` and `alt` into the dialog and calls `showModal()`.
5. Closes on the explicit button, a click whose target is the dialog backdrop, or the native Escape cancel behavior.
6. Remembers the opening button and focuses it from the dialog's `close` handler.
7. Enhances the initial document and subscribes to Material's `document$` stream so client-side navigation receives the same behavior without duplicate wrappers.

In `docs/stylesheets/extra.css`, move the light/dark display rules to the trigger modifiers, make triggers inherit the figure width with a zoom-in cursor and visible keyboard focus, and style the dialog, backdrop, close button, and full-size image. Keep the existing inline border, radius, and shadow on the images.

- [x] **Step 4: Run focused rendered-site tests and verify GREEN**

Rebuild the docs, then run the focused tests:

```bash
node scripts/build-docs.mjs
env KENN_FORGE_DOCS_SITE_DIR=site \
  node node_modules/vite-plus/bin/vp exec -- playwright test \
  --config docs/site/playwright.config.ts --project=chromium \
  --grep "generated workflow screenshot"
```

Expected: the lightbox interaction and dark-theme asset tests pass.

---

### Task 3: Freeze an authentic synthetic Codex transcript

**Files:**
- Modify: `docs/screenshots/docs-screenshots.spec.ts`
- Modify: `docs/screenshots/README.md`

**Interfaces:**
- Consumes: a one-time `codex exec --ephemeral` run in an isolated synthetic widget-cache repository.
- Produces: deterministic transcript and TUI footer DOM that survives screenshot SVG serialization.

- [ ] **Step 1: Run real Codex once in a synthetic repository**

Create an ignored scratch Git repository containing a deliberately incomplete widget cache and Node built-in tests for successful-result caching, concurrent-load coalescing, and failed-load eviction. Run the installed Codex CLI with `--ephemeral`, `--ignore-user-config`, and `--sandbox workspace-write`, asking it to implement the cache and run `node --test`.

- [ ] **Step 2: Sanitize a short transcript fixture**

Extract only generic task, inspection, implementation, and passing-test lines. Remove the scratch path, session identifiers, timing/token data, environment details, and any user or real-repository data. Keep the text short enough to remain legible in the selected Codex pane.

- [ ] **Step 3: Write a failing screenshot-content assertion**

Require the `workspace-codex-session` and `maintainer-overview` SVGs to contain the frozen task summary, passing-test result, prompt composer, model, and sanitized repository path. Run the light maintainer capture and verify it fails while the terminal canvas remains absent from DOM serialization.

- [ ] **Step 4: Inject the frozen Codex view into the terminal DOM**

Inject a static terminal overlay containing the sanitized transcript and the composer/model/path footer captured from the one-off real session. Keep the synthetic target's long-running wait only for session lifecycle. Do not invoke Codex from the generated screenshot suite or Vercel build because terminal canvas pixels are not preserved by the SVG serializer.

- [ ] **Step 5: Verify and inspect both themed captures**

Run both `maintainer-overview` captures, assert the frozen transcript is embedded in each SVG, and visually confirm the coding-agent pane is readable in light and dark mode.

---

### Task 4: Verify and deliver both deployment pull requests

**Files:**
- Verify all Forge branch changes already in the working tree.
- Verify in a checkout of `kenn-io/kenn-ops`: `workspaces/kenn-io-website/main.tf`
- Verify in a checkout of `kenn-io/kenn-ops`: `workspaces/kenn-io-website/tests/smoke.tftest.hcl`

**Interfaces:**
- Consumes: `scripts/vercel-build-docs.sh`, `vercel.json`, and the already-applied `forge.kenn.io` infrastructure definition.
- Produces: one verified Vercel preview and updated Forge PR #830 and kenn-ops PR #103.

- [ ] **Step 1: Run the complete local docs verification**

Run the script unit tests, branding guard, all generated captures, rendered-site tests, formatting checks, shell checks, and `git diff --check`. The exact build wrapper `scripts/vercel-build-docs.sh` must finish with 12 screenshot cases and the complete rendered-site suite passing.

- [ ] **Step 2: Inspect the rendered behavior locally**

Serve `site/`, inspect the home page in light and dark themes, open and close the lightbox by each supported path, and confirm the first image shows Activity, selected PR, live workspace, and selected Codex session.

- [ ] **Step 3: Create one final Vercel preview**

Run Vercel from the Forge worktree with the linked `kenn-forge-docs` project. Wait for completion and verify HTTP 200, the canonical favicon, the active-theme lightbox asset, the first workflow composition, and successful screenshot/site tests in the build log.

- [x] **Step 4: Validate kenn-ops without applying infrastructure**

From `workspaces/kenn-io-website` in the kenn-ops checkout, run `tofu fmt -check -diff .`, `tofu validate`, `tofu test -test-directory=tests`, and `git diff --check`. Do not apply OpenTofu and do not stage `.kata.toml`. Infrastructure changes remain a separate commit and PR in that repository.

- [ ] **Step 5: Commit and push the final changes**

For Forge, run `context-sync --commit`, then the mandatory commit skill with hook-enforced commits. For kenn-ops, run its repository-required commit workflow. Never amend and never use `--no-verify`. Push the existing `t3code/add-docs-deploy-path` and `forge-docs-deploy` branches.

- [ ] **Step 6: Update both existing PRs**

Update Forge PR #830 and kenn-ops PR #103 with the direct Vercel Git-build architecture, generated-workflow-screenshot lightbox, first-image composition, DNS target, and exact verification evidence. End every agent-authored GitHub comment or description with `<sup>generated by a clanker</sup>`.
