# Documentation Navigation and System Theme Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Fleet under an advanced/experimental navigation group and make the rendered documentation follow the browser color scheme before first paint while preserving explicit user overrides.

**Approved spec/design:** `docs/superpowers/specs/2026-08-02-public-documentation-readiness-design.md`

**Architecture:** Keep Fleet's page and URL unchanged; only the Zensical navigation hierarchy changes. Use media-qualified light and dark palette entries, plus a small staged Zensical `main.html` override that applies the matching or stored palette from the document head. Until the reader interacts with the palette control, the override prevents Zensical's runtime from turning an automatic match into a persisted manual choice.

**Tech Stack:** Zensical TOML and Jinja templates, Node.js staging script/tests, Playwright rendered-site tests.

## Global Constraints

- Fleet appears under **Advanced / experimental**, not beside the main setup and workflow pages.
- Existing Fleet URLs and Markdown cross-links remain unchanged.
- A first visit follows `prefers-color-scheme: light` or `prefers-color-scheme: dark` before the runtime bundle loads.
- Automatic selection keeps following later browser preference changes until the reader uses the palette control.
- An explicit light or dark choice persists and overrides the browser preference.
- Internal plans, specs, ADRs, reports, and screenshot tooling remain excluded from staged and rendered output.
- Generated SVG screenshots remain untracked.

---

### Task 0: Commit the reviewed plan

**Files:**
- Create: `docs/superpowers/plans/2026-08-03-docs-navigation-system-theme.md`

- [ ] **Step 1: Apply the read-only plan review**

Resolve every supported finding from RoboRev task `9469`. Run:

```bash
git diff --check
```

- [ ] **Step 2: Commit the plan**

Invoke repository-local `context-sync --commit`, then invoke the mandatory
commit skill. Stage only this plan and use the subject
`docs: plan final public site corrections`, with a rationale body and Codex
attribution. Do not run a separate subject-only commit command.

---

### Task 1: Nest Fleet under advanced navigation

**Files:**
- Modify: `docs/site/docs-site.spec.ts`
- Modify: `docs/zensical.toml`

**Interfaces:**
- Consumes: the rendered primary navigation under `.md-sidebar--primary`
- Produces: an **Advanced / experimental** navigation section whose nested Fleet link still resolves to `/federated-fleet/`

- [ ] **Step 1: Add the failing rendered-navigation test**

Append this test to `docs/site/docs-site.spec.ts`:

```ts
test("places Fleet under advanced and experimental navigation", async ({ page }) => {
  await page.goto("/");

  const primaryNav = page.locator(".md-sidebar--primary nav[data-md-level='0']");
  const advancedLabel = primaryNav.locator("label.md-nav__link", {
    hasText: "Advanced / experimental",
  });
  await expect(advancedLabel).toBeVisible();

  const advancedItem = advancedLabel.locator("xpath=ancestor::li[1]");
  await expect(advancedItem.getByRole("link", { name: "Fleet" })).toHaveAttribute(
    "href",
    /federated-fleet\/$/,
  );
  await expect(
    primaryNav.locator(":scope > ul > li > a.md-nav__link", { hasText: "Fleet" }),
  ).toHaveCount(0);
});
```

- [ ] **Step 2: Run the docs build and verify the navigation test fails**

Run:

```bash
node scripts/build-docs.mjs
```

Expected: the screenshot cases and Zensical build pass, then the rendered-site project fails because **Advanced / experimental** does not exist.

- [ ] **Step 3: Move Fleet into the nested navigation group**

Replace the top-level Fleet entry in `docs/zensical.toml` with:

```toml
  {"Advanced / experimental" = [
    {"Fleet" = "federated-fleet.md"},
  ]},
```

Keep Archive and Commands at their existing top-level positions.

- [ ] **Step 4: Rebuild and verify the navigation test passes**

Run:

```bash
node scripts/build-docs.mjs
```

Expected: 12 screenshot tests, Zensical, and all rendered-site tests pass. Inspect `site/index.html` and confirm Fleet appears only inside the nested section.

- [ ] **Step 5: Commit the navigation change**

Invoke repository-local `context-sync --commit`, then invoke the mandatory
commit skill. Stage only `docs/site/docs-site.spec.ts` and `docs/zensical.toml`.
Use the subject `docs: classify Fleet as advanced functionality`, with a
rationale body and Codex attribution. Do not run a separate subject-only commit
command or amend earlier commits.

---

### Task 2: Apply system theme before the runtime bundle

**Files:**
- Create: `docs/overrides/main.html`
- Modify: `docs/site/docs-site.spec.ts`
- Modify: `docs/zensical.toml`
- Modify: `scripts/build-docs.mjs`
- Modify: `scripts/build-docs.test.mjs`

**Interfaces:**
- Consumes: Zensical's `__md_get("__palette")` and `__md_set`, palette change events, and browser `matchMedia`
- Produces: a head-level bootstrap that sets body `data-md-color-*` attributes before the first animation frame, follows system changes while no stored choice exists, and leaves explicit user choices to Zensical's normal persistence path

- [ ] **Step 1: Add failing system-theme and persistence tests**

Add this type near the top of `docs/site/docs-site.spec.ts`:

```ts
type FirstFrameWindow = Window & {
  __firstFrameScheme?: Promise<string | null>;
};
```

Append these tests:

```ts
test("applies the browser theme before the runtime bundle", async ({ browser }) => {
  for (const preference of [
    { colorScheme: "light" as const, expected: "default" },
    { colorScheme: "dark" as const, expected: "slate" },
  ]) {
    const context = await browser.newContext({ colorScheme: preference.colorScheme });
    await context.addInitScript(() => {
      const target = window as FirstFrameWindow;
      target.__firstFrameScheme = new Promise((resolve) => {
        requestAnimationFrame(() => {
          resolve(document.body?.getAttribute("data-md-color-scheme") ?? null);
        });
      });
    });
    const page = await context.newPage();
    await page.route("**/assets/javascripts/bundle.*.min.js", (route) => route.abort());

    await page.goto("/");
    const firstFrameScheme = await page.evaluate(() =>
      (window as FirstFrameWindow).__firstFrameScheme,
    );
    expect(firstFrameScheme).toBe(preference.expected);
    await context.close();
  }
});

test("keeps following system theme changes until the reader chooses", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "slate");

  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");

  await page.reload();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");
  await context.close();
});

test("persists an explicit light override on a dark system", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "slate");

  await page.locator('label[title="Switch to light mode"]').click();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");

  await page.reload();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");
  await context.close();
});
```

- [ ] **Step 2: Run the docs build and verify the dark-preference assertions fail**

Run:

```bash
node scripts/build-docs.mjs
```

Expected: the first-frame dark case and initial dark assertions fail because both current palette entries use `media = "none"` and the body starts with `data-md-color-scheme="default"`.

- [ ] **Step 3: Add the palette override to the staged-source contract test**

In `scripts/build-docs.test.mjs`, create
`overrides/main.html` in the synthetic source, write
`<script>palette</script>\n`, and assert that exact content is staged. Also
write `overrides/internal.html` and include it in the existing rejection list.

Run:

```bash
node node_modules/vite-plus/bin/vp exec -- node --test scripts/build-docs.test.mjs
```

Expected: FAIL because the current public staging allowlist excludes the override tree.

- [ ] **Step 4: Allowlist only the palette override**

In `scripts/build-docs.mjs`, add this exact file to `publishedFiles`:

```js
path.join("overrides", "main.html")
```

Add only the required parent directory to `publishedDirectoryEntries`:

```js
"overrides",
```

Rerun the focused script test. Expected: PASS, while `overrides/internal.html`
remains excluded.

- [ ] **Step 5: Configure media-qualified palettes and the custom template directory**

In `docs/zensical.toml`, add:

```toml
[project.theme]
custom_dir = "docs/overrides"
```

Keep the existing `variant` and `features` entries in the same table. Add
`media = "(prefers-color-scheme: light)"` to the `default` palette and
`media = "(prefers-color-scheme: dark)"` to the `slate` palette. Keep the
existing toggle icons and names.

- [ ] **Step 6: Add the synchronous palette bootstrap override**

Create `docs/overrides/main.html` with:

```html
{% extends "base.html" %}
{% block extrahead %}
  <script>
    (function () {
      var stored = __md_get("__palette");
      var color = stored && stored.color;
      if (!color) {
        var light = matchMedia("(prefers-color-scheme: light)").matches;
        color = {
          media: light ? "(prefers-color-scheme: light)" : "(prefers-color-scheme: dark)",
          scheme: light ? "default" : "slate",
          primary: "custom",
          accent: "custom",
        };

        var persist = __md_set;
        var readerSelected = false;
        document.addEventListener("change", function (event) {
          if (event.target && event.target.name === "__palette") readerSelected = true;
        }, true);
        __md_set = function (key, value, storage, scope) {
          if (key === "__palette" && !readerSelected) return;
          persist(key, value, storage, scope);
        };
      }

      new MutationObserver(function (_, observer) {
        if (!document.body) return;
        for (var key in color) document.body.setAttribute("data-md-color-" + key, color[key]);
        observer.disconnect();
      }).observe(document.documentElement, { childList: true });
    })();
  </script>
{% endblock %}
```

Keep the override self-contained and dependency-free. It must not write a
palette choice. It may suppress Zensical's automatic palette write only while
there is no stored choice and no palette change event from the reader.

- [ ] **Step 7: Rebuild and verify theme selection and persistence**

Run:

```bash
node scripts/build-docs.mjs
```

Expected: 12 screenshot tests, Zensical, and all rendered-site tests pass. The
bundle-blocked dark context must report `slate` at its first animation frame.
Changing an untouched page from dark to light must survive reload without
creating a manual override.

- [ ] **Step 8: Inspect both browser preferences in the embedded browser**

Keep the existing validated server on `127.0.0.1:4177`. In the embedded browser,
open `/workflows/code-reviewer/` at desktop and phone widths. Confirm the page
and matching workflow SVG use the active appearance, Fleet is reachable only
inside **Advanced / experimental**, and a manual palette choice survives a
reload. Use the rendered-site Playwright cases for deterministic light and dark
emulation.

- [ ] **Step 9: Commit the theme change**

Invoke repository-local `context-sync --commit`, then invoke the mandatory
commit skill. Stage only `docs/overrides/main.html`,
`docs/site/docs-site.spec.ts`, `docs/zensical.toml`, `scripts/build-docs.mjs`,
and `scripts/build-docs.test.mjs`. Use the subject
`fix: respect the browser theme in public docs`, with a rationale body and
Codex attribution. Do not run a separate subject-only commit command or amend
earlier commits.

---

### Task 3: Verify and publish the correction

**Files:**
- No repository file changes expected

**Interfaces:**
- Consumes: the committed navigation and theme behavior
- Produces: a clean pushed branch updating PR #821

- [ ] **Step 1: Run final verification**

Run:

```bash
base=$(git merge-base HEAD origin/t3code/design-onboarding-mockups)
git diff --check "$base"...HEAD
node node_modules/vite-plus/bin/vp exec -- node --test scripts/*.test.mjs scripts/*.test.ts
node scripts/build-docs.mjs
test -z "$(git ls-files 'site/**' 'docs/assets/generated/**')"
if find site -type f \( -path '*/superpowers/*' -o -path '*/adr/*' -o -path '*/reports/*' -o -path '*/screenshots/*' \) -print -quit | grep -q .; then exit 1; fi
git status --short
```

Expected: script tests pass, the docs build reports 12 screenshot cases and all
rendered-site cases passing, internal trees remain absent from `site/`, and
generated SVGs remain untracked. Inspect every generated light/dark SVG for
clipping, loading state, and synthetic content. Use the embedded browser to
visit every primary navigation page at 1280x800 and 390x844; check headings,
code blocks, internal links, image paths, alt text, and theme switching.

- [ ] **Step 2: Scrub and push**

Inspect the complete diff and new commit messages for contributor home paths,
private hosts, credentials, runner details, and non-synthetic project names.
Push `t3code/refine-onboarding-documentation` to `origin` without polling CI.

- [ ] **Step 3: Verify the running preview and PR**

Confirm the embedded browser is visible on the live rendered site, the serving
process still listens on port `4177`, and run:

```bash
gh pr view 821 --json baseRefName,headRefName,url
```

Confirm the base is `t3code/design-onboarding-mockups` and the head is
`t3code/refine-onboarding-documentation`.
