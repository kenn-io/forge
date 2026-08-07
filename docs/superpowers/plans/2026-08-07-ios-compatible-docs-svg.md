# iOS-Compatible Documentation SVG Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace WebKit-incompatible XHTML-in-SVG screenshots with crisp native SVG build artifacts.

**Approved spec/design:** `docs/superpowers/specs/2026-08-07-ios-compatible-docs-svg-design.md`

**Architecture:** Print each stabilized Chromium page to a one-page vector PDF, convert it with `pdftocairo`, and normalize the resulting SVG metadata and dimensions. Keep the public asset names and docs presentation unchanged.

**Tech Stack:** Playwright/Chromium, Node.js, Poppler `pdftocairo`, Node test runner, Zensical

## Global Constraints

- Preserve `.svg` filenames, themes, captions, lightbox behavior, seeded data, and 1280 by 820 intrinsic dimensions.
- Generated screenshot files remain untracked build artifacts.
- Reject `<foreignObject>`, scripts, remote assets, private paths, and active animation in generated SVGs.
- Poppler is build-only and failure is fatal with a clear diagnostic.
- Amazon Linux 2 Poppler 0.26.5 is the converter compatibility floor.

---

### Task 1: Pin the native-SVG regression

**Files:**

- Modify: `docs/screenshots/docs-screenshots.spec.ts`

**Interfaces:**

- Consumes: the existing `captureCase` generated SVG string
- Produces: an output-contract assertion that rejects XHTML-backed SVGs

- [ ] **Step 1: Add the failing output assertion**

Add `expect(svg).not.toMatch(/<foreignObject\\b/)` beside the existing
generated-asset safety assertions.

- [ ] **Step 2: Run one screenshot case and verify RED**

Run:

```bash
mkdir -p tmp/docs-svg-red
KENN_FORGE_DOCS_SCREENSHOT_DIR=tmp/docs-svg-red vp exec -- playwright test \
  --config docs/screenshots/playwright.config.ts --project=chromium \
  --grep "maintainer-overview light"
```

Expected: FAIL because the current output contains `<foreignObject>`.

### Task 2: Export native SVG geometry

**Files:**

- Modify: `docs/screenshots/docs-screenshots.spec.ts`

**Interfaces:**

- Consumes: `Page` plus `{ title, description, width, height }`
- Produces: `nativeSVGSnapshot(page, input): Promise<string>`

- [ ] **Step 1: Replace `svgDOMSnapshot` with the native exporter**

Use `page.emulateMedia({ media: "screen" })`, `page.pdf()` with zero margins,
exact viewport dimensions, and printed backgrounds. Write the PDF to a unique
temporary directory, invoke:

```text
pdftocairo -svg -f 1 -l 1 INPUT.pdf OUTPUT.svg
```

Read the SVG, set root `width="1280"` and `height="820"`, add escaped `<title>`
and `<desc>` elements, then remove the temporary directory in `finally`.

- [ ] **Step 2: Update output assertions for native geometry**

Retain page-state assertions before export. Remove assertions that depend on
the cloned XHTML source text or class names. Retain safety assertions for
syncing state, external URLs, raster page captures, local paths, and add the
`<foreignObject>` rejection.

- [ ] **Step 3: Run the focused screenshot case and verify GREEN**

Run the Task 1 command again.

Expected: PASS and `tmp/docs-svg-red/maintainer-overview-light.svg` contains
native SVG geometry with no `<foreignObject>`.

### Task 3: Provision Poppler in build environments

**Files:**

- Modify: `scripts/vercel-install-docs.sh`
- Modify: `.github/docker/playwright/Dockerfile`
- Modify: `docs/README.md`

**Interfaces:**

- Consumes: the existing Vercel `dnf` and Playwright-image `apt-get` package installation steps
- Produces: `pdftocairo` on every automated docs-build PATH

- [ ] **Step 1: Add the build-only packages**

Add `poppler-utils` to both existing system-package install commands. Document
Poppler as a local prerequisite for `make docs-build`.

- [ ] **Step 2: Exercise the real converter path**

Run:

```bash
pdftocairo -v
node --test scripts/build-docs.test.mjs scripts/docs-screenshots-command.test.mjs
```

Expected: converter reports its version and all script tests pass.

- [ ] **Step 3: Verify the Amazon Linux 2 converter floor**

Run the proof PDF through `poppler-utils-0.26.5-43.amzn2.1.7` in an official
`amazonlinux:2` container and load the resulting SVG in an iPhone-sized WebKit
context. Expected: intrinsic size 1280 by 820, zero `<foreignObject>` elements,
and complete content scaled inside the responsive image box.

### Task 4: Verify the complete docs build and WebKit rendering

**Files:**

- No additional source files

**Interfaces:**

- Consumes: all twelve generated assets and rendered `site/`
- Produces: build and browser evidence for the user-visible fix

- [ ] **Step 1: Run the complete docs build**

Run:

```bash
make docs-build
```

Expected: twelve screenshot cases, Zensical build, and rendered-site suite pass.

- [ ] **Step 2: Verify asset invariants**

Run:

```bash
rg -L '<foreignObject' site/assets/generated/*.svg
git diff --check
```

Expected: every generated asset is listed by `rg -L`; diff check passes.

- [ ] **Step 3: Verify WebKit at phone size**

Serve `site/`, load `/` through Playwright WebKit with an iPhone device profile,
and assert the visible workflow image has natural dimensions 1280 by 820 and
its complete vector content scales into the responsive image box. Capture a
local screenshot for visual inspection.

- [ ] **Step 4: Run final repository checks**

Run the repository's non-mutating lint command covering the changed TypeScript,
JavaScript, shell, Markdown, and Docker files, followed by `git status --short`.

Expected: all checks pass and only intended files are modified.
