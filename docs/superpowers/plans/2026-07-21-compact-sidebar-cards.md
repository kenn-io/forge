# Compact 2-Line Sidebar Cards Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make PR/issue sidebar cards a fixed 2-line layout (~50px on desktop) so 50–60% more items fit on screen.

**Architecture:** Restructure `PullItem.svelte` / `IssueItem.svelte` so labels collapse to inline color dots on the title line (new `dots` variant of `LabelRow`) and the repo chip moves into the meta row. Mobile keeps its `.mobile-main` sizing overrides but adopts the same structure. Spec: `docs/superpowers/specs/2026-07-21-compact-sidebar-cards-design.md`.

**Tech Stack:** Svelte 5 (runes), Vitest via Vite+ (`vp test`), Playwright e2e against the seeded Go e2e server.

## Global Constraints

- Never use npm. Frontend tooling runs via `node node_modules/vite-plus/bin/vp ...` from the repo root (or `../node_modules/vite-plus/bin/vp` from `frontend/`).
- Before every commit, invoke the repo `context-sync` skill with `--commit`, then commit normally (pre-commit hooks must pass; never `--no-verify`, never `--amend`).
- Commit subjects: conventional, imperative, ≤72 chars, explain the user-visible outcome.
- No emojis. Font sizes must use design tokens (`--font-size-*`) — a pre-commit hook enforces this; raw `px` font sizes will be rejected (other px dimensions are fine).
- Do not touch `KanbanCard.svelte`, `WorkspacePanelPRItem.svelte`, Kata/Docs/Messages components, or the global `--sidebar-row-padding` in `frontend/src/app.css`.
- All work happens on the current branch `boiled-subway`. Do not create or switch branches.

---

### Task 1: `LabelRow` dots variant

**Files:**
- Modify: `packages/ui/src/components/shared/LabelRow.svelte`
- Test: `packages/ui/src/components/shared/LabelRow.test.ts`

**Interfaces:**
- Consumes: nothing new.
- Produces: `LabelRow` prop `dots?: boolean` (default false). When true, renders `<span class="label-dots" title="<name>, <name>" aria-hidden="true">` containing up to 4 `<span class="label-dot">` children, plus an adjacent `<span class="kit-sr-only">Labels: <names></span>`. Existing default and `compact` renderings are unchanged. `normalizeLabelColor` stays private to the module script (no caller imports it; tests assert through rendered styles).

- [ ] **Step 1: Write the failing tests**

Append to `packages/ui/src/components/shared/LabelRow.test.ts` (inside the existing file, after the current `describe` block):

```ts
describe("LabelRow dots variant", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders nothing without labels", () => {
    const { container } = render(LabelRow, { props: { labels: [], dots: true } });
    expect(container.querySelector(".label-dots")).toBeNull();
  });

  it("renders one dot per label with names in tooltip and sr-only text", () => {
    const { container } = render(LabelRow, { props: { labels: labels.slice(0, 2), dots: true } });
    expect(container.querySelectorAll(".label-dot")).toHaveLength(2);
    expect(container.querySelector(".label-dots")?.getAttribute("title")).toBe("bug, enhancement");
    expect(container.querySelector(".label-dots")?.getAttribute("aria-hidden")).toBe("true");
    expect(screen.getByText("Labels: bug, enhancement")).toBeTruthy();
    expect(screen.queryByText("bug")).toBeNull();
  });

  it("caps dots at four with no overflow indicator, tooltip still lists all names", () => {
    const five = [...labels, { name: "extra", color: "ffffff" }];
    const { container } = render(LabelRow, { props: { labels: five, dots: true } });
    expect(container.querySelectorAll(".label-dot")).toHaveLength(4);
    expect(screen.queryByText(/^\+\d+$/)).toBeNull();
    expect(container.querySelector(".label-dots")?.getAttribute("title")).toBe(
      "bug, enhancement, docs, help wanted, extra",
    );
  });

  it("normalizes bare, prefixed, 3-digit, and invalid hex colors", () => {
    const { container } = render(LabelRow, {
      props: {
        labels: [
          { name: "bare", color: "d73a4a" },
          { name: "prefixed", color: "#A2EEEF" },
          { name: "short", color: "0aF" },
          { name: "invalid", color: "not-a-color" },
        ],
        dots: true,
      },
    });
    const styles = [...container.querySelectorAll(".label-dot")].map((n) => n.getAttribute("style") ?? "");
    expect(styles[0]).toContain("#d73a4a");
    expect(styles[1]).toContain("#a2eeef");
    expect(styles[2]).toContain("#00aaff");
    expect(styles[3]).toContain("#6e7781");
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run from `frontend/` (the unit project is defined in `frontend/vite.config.ts` and includes `../packages/ui` tests):
```bash
cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/shared/LabelRow.test.ts
```
Expected: the 4 new tests FAIL (no `.label-dot` elements rendered); the 4 existing tests PASS.

- [ ] **Step 3: Implement the dots variant**

Replace `packages/ui/src/components/shared/LabelRow.svelte` with:

```svelte
<script module lang="ts">
  /* Raw hex is intentional: label colors are caller-supplied hex values,
   * not theme tokens. Mirrors kit-ui ColorLabel's private normalization. */
  const DOT_FALLBACK_COLOR = "#6e7781";

  /** Expands/validates a hex label color (with or without `#`, 3 or 6
   * digits). Invalid input falls back to a neutral gray. */
  function normalizeLabelColor(color: string): string {
    const hex = color.trim().replace(/^#/, "");

    if (/^[0-9a-fA-F]{3}$/.test(hex)) {
      return `#${hex
        .split("")
        .map((char) => `${char}${char}`)
        .join("")
        .toLowerCase()}`;
    }

    if (/^[0-9a-fA-F]{6}$/.test(hex)) {
      return `#${hex.toLowerCase()}`;
    }

    return DOT_FALLBACK_COLOR;
  }
</script>

<script lang="ts">
  import { ColorLabel } from "@kenn-io/kit-ui";
  import type { Label } from "../../api/types.js";

  interface Props {
    labels: Pick<Label, "name" | "color">[];
    /** Compact rows (sidebar list items) show the first two labels plus a
     * passive +N overflow and cap pill width; the default row wraps. */
    compact?: boolean;
    /** Dots render one small color circle per label (max 4) with names in
     * the tooltip and screen-reader text — used inline on title lines. */
    dots?: boolean;
  }

  let { labels, compact = false, dots = false }: Props = $props();

  const visible = $derived(compact ? labels.slice(0, 2) : labels);
  const overflow = $derived(labels.length - visible.length);
  const dotLabels = $derived(labels.slice(0, 4));
  const labelNames = $derived(labels.map((l) => l.name).join(", "));
</script>

{#if labels.length > 0}
  {#if dots}
    <span class="label-dots" title={labelNames} aria-hidden="true">
      {#each dotLabels as label (label.name)}
        <span class="label-dot" style="background: {normalizeLabelColor(label.color)}"></span>
      {/each}
    </span>
    <span class="kit-sr-only">Labels: {labelNames}</span>
  {:else}
    <span class={["label-row", compact && "label-row--compact"]}>
      {#each visible as label (label.name)}
        {#if compact}
          <ColorLabel size="sm" name={label.name} color={label.color} />
        {:else}
          <ColorLabel name={label.name} color={label.color} />
        {/if}
      {/each}
      {#if overflow > 0}
        <span class="label-more">+{overflow}</span>
      {/if}
    </span>
  {/if}
{/if}

<style>
  .label-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
  }

  .label-row--compact {
    flex-wrap: nowrap;
    overflow: hidden;
  }

  .label-row--compact :global(.kit-color-label) {
    max-width: 120px;
  }

  .label-more {
    flex-shrink: 0;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  .label-dots {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    flex-shrink: 0;
  }

  .label-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
  }
</style>
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/shared/LabelRow.test.ts
```
Expected: all 8 tests PASS.

- [ ] **Step 5: Commit**

Invoke the `context-sync` skill with `--commit`, then:
```bash
git add packages/ui/src/components/shared/LabelRow.svelte packages/ui/src/components/shared/LabelRow.test.ts
git commit -m "feat: add dots variant to LabelRow for compact sidebar rows"
```
Body: note that kit-ui ColorLabel's hex normalization is private to the pinned external package, so LabelRow implements the equivalent for raw dot backgrounds.

---

### Task 2: PullItem compact layout

**Files:**
- Modify: `packages/ui/src/components/sidebar/PullItem.svelte`
- Test: `packages/ui/src/components/sidebar/PullItem.test.ts`

**Interfaces:**
- Consumes: `LabelRow` `dots` prop from Task 1.
- Produces: DOM contract used by Task 4's Playwright assertions — `.pull-item > .title` contains `.state-dot`, `.title-text`, and (when labeled) `.label-dots`; `.meta-row .meta-left` contains the `.repo-chip` (when `showRepo`) followed by `.meta-text`; `.repo-row` no longer exists in this component. Props are unchanged.

- [ ] **Step 1: Write the failing tests**

Append to `packages/ui/src/components/sidebar/PullItem.test.ts`:

```ts
describe("PullItem compact layout", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders the repo chip inside the meta row with no separate repo row", () => {
    render(PullItem, {
      props: {
        pr: mkPR({}),
        selected: false,
        showRepo: true,
        repoLabel: "o/n",
        onclick: () => {},
      },
      context: new Map<symbol, unknown>([
        [STORES_KEY, { pulls: { togglePRStar: vi.fn() } }],
        [HOST_STATE_KEY, {}],
      ]),
    });

    expect(document.querySelector(".meta-row .meta-left .kit-chip.repo-chip")).not.toBeNull();
    expect(document.querySelector(".repo-row")).toBeNull();
  });

  it("renders label dots on the title line instead of a label pill row", () => {
    renderItem(
      mkPR({
        labels: [
          { name: "bug", color: "d73a4a" },
          { name: "sync", color: "0075ca" },
        ],
      }),
    );

    expect(document.querySelectorAll(".title .label-dot")).toHaveLength(2);
    expect(document.querySelector(".title .label-dots")?.getAttribute("title")).toBe("bug, sync");
    expect(screen.getByText("Labels: bug, sync")).toBeTruthy();
    expect(document.querySelector(".label-row")).toBeNull();
    expect(screen.queryByText("bug")).toBeNull();
  });

  it("keeps title text and meta number/author in the two-line structure", () => {
    renderItem(mkPR({ Title: "Cache widget details", Author: "alice", Number: 7 }));

    expect(document.querySelector(".title .title-text")?.textContent).toBe("Cache widget details");
    expect(document.querySelector(".meta-left .meta-text")?.textContent).toContain("#7 · alice");
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/sidebar/PullItem.test.ts
```
Expected: the 3 new tests FAIL (`.title-text` and `.meta-text` don't exist; chip renders in `.repo-row`); all existing tests PASS.

- [ ] **Step 3: Restructure the markup**

In `packages/ui/src/components/sidebar/PullItem.svelte`, replace the template block from `<p class="title">` through the closing `{/if}` after the repo `Chip` (currently the title, `<LabelRow {labels} compact />`, and the `repo-row` block) with:

```svelte
  <p class="title">
    <span class="state-dot" style="background: {stateColors[prState]}"></span>
    <span class="title-text">{pr.Title}</span>
    <LabelRow {labels} dots />
  </p>
  <div class="meta-row">
    <span class="meta-left">
      {#if showRepo}
        <Chip
          size="xs"
          uppercase={false}
          title={repoLabel}
          tone="muted" class="repo-chip"
          style={`color: ${hashColor(repoColorKey)}; background: color-mix(in srgb, ${hashColor(repoColorKey)} 15%, transparent);`}
        >{repoLabel}</Chip>
      {/if}
      <span class="meta-text">#{pr.Number} · {pr.Author}</span>
    </span>
```

The `<div class="meta-row">` open tag and `.meta-left` span replace the existing ones; everything from `<span class="meta-right">` onward is unchanged.

- [ ] **Step 4: Update the styles**

In the same file's `<style>` block:

Replace the `.pull-item` padding line:
```css
    padding: 6px 10px;
```

Replace the `.title` rule and add `.title-text`:
```css
  .title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: var(--font-size-md);
    font-weight: 500;
    color: var(--text-primary);
    overflow: hidden;
    margin-bottom: 2px;
  }

  .title-text {
    flex: 0 1 auto;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
```

Replace the `.meta-left` rule and add `.meta-text` (font styling moves to the text span; the container becomes a flex row):
```css
  .meta-left {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .meta-text {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
```

Delete the `.repo-row` rule. Replace the `:global(.kit-chip.repo-chip)` rule so the chip shrinks before the number/author text and never exceeds ~45% of the row:
```css
  :global(.kit-chip.repo-chip) {
    flex: 0 4 auto;
    justify-content: flex-start;
    min-width: 0;
    max-width: 45%;
    overflow: hidden;
  }
```

Mobile overrides — the structure is shared, only sizing differs:
- In `:global(.mobile-main) .pull-item`, change the min-height multiplier from `* 1.65` to `* 1.95` (the removed repo/label rows made rows shorter; ~72px keeps mobile rows comfortably tappable and keeps the existing mobile e2e readability assertion meaningful).
- In `:global(.mobile-main) .title`, remove the `white-space: normal`, `display: -webkit-box`, `-webkit-box-orient`, `-webkit-line-clamp`, and `line-clamp` declarations (keep gap, margin, font-size, line-height) and add a new rule carrying the clamp on the text span:
```css
  :global(.mobile-main) .title-text {
    white-space: normal;
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
  }
```
- Delete the `:global(.mobile-main) .repo-row` rule.
- In the `:global(.mobile-main) .meta-left, ...` font-size rule, replace `.meta-left` with `.meta-text`.

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/sidebar/PullItem.test.ts
```
Expected: all tests PASS (including the pre-existing CI-cluster and kanban describes — the meta-right cluster is untouched).

- [ ] **Step 6: Commit**

Invoke `context-sync --commit`, then:
```bash
git add packages/ui/src/components/sidebar/PullItem.svelte packages/ui/src/components/sidebar/PullItem.test.ts
git commit -m "feat: compact PR sidebar cards to a two-line layout"
```
Body: more PRs fit on screen; labels collapse to title-line dots and the repo chip joins the meta row; mobile keeps its sizing overrides with a min-height floor replacing the removed rows.

---

### Task 3: IssueItem compact layout

**Files:**
- Modify: `packages/ui/src/components/sidebar/IssueItem.svelte`
- Test: `packages/ui/src/components/sidebar/IssueItem.test.ts`

**Interfaces:**
- Consumes: `LabelRow` `dots` prop from Task 1.
- Produces: same DOM contract as Task 2 for `.issue-item` (no `.state-dot` — issues never had one): `.title > .title-text` + `.label-dots`, `.meta-row .meta-left > .repo-chip? + .meta-text`, no `.repo-row`.

- [ ] **Step 1: Write the failing tests**

Append to `packages/ui/src/components/sidebar/IssueItem.test.ts` (inside the existing `describe("IssueItem", ...)` block, after the workspace indicator test):

```ts
  it("renders the repo chip inside the meta row with no separate repo row", () => {
    render(IssueItem, {
      props: {
        issue: mkIssue({}),
        selected: false,
        showRepo: true,
        repoLabel: "acme/widgets",
        onclick: () => {},
      },
      context: new Map<symbol, unknown>([[STORES_KEY, { issues: { toggleIssueStar: vi.fn() } }]]),
    });

    expect(document.querySelector(".meta-row .meta-left .kit-chip.repo-chip")).not.toBeNull();
    expect(document.querySelector(".repo-row")).toBeNull();
  });

  it("renders label dots on the title line instead of a label pill row", () => {
    renderItem(
      mkIssue({
        labels: [
          { name: "bug", color: "d73a4a" },
          { name: "docs", color: "0075ca" },
        ],
      }),
    );

    expect(document.querySelectorAll(".title .label-dot")).toHaveLength(2);
    expect(document.querySelector(".title .label-dots")?.getAttribute("title")).toBe("bug, docs");
    expect(screen.getByText("Labels: bug, docs")).toBeTruthy();
    expect(document.querySelector(".label-row")).toBeNull();
  });

  it("keeps title text and meta number/author in the two-line structure", () => {
    renderItem(mkIssue({}));

    expect(document.querySelector(".title .title-text")?.textContent).toBe("Track workspace setup");
    expect(document.querySelector(".meta-left .meta-text")?.textContent).toContain("#2 · alice");
  });
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/sidebar/IssueItem.test.ts
```
Expected: the 3 new tests FAIL; the workspace indicator test PASSES.

- [ ] **Step 3: Restructure markup and styles**

In `packages/ui/src/components/sidebar/IssueItem.svelte`, replace the template block from `<p class="title">` through the `{/if}` closing the repo-row block with:

```svelte
  <p class="title">
    <span class="title-text">{issue.Title}</span>
    <LabelRow {labels} dots />
  </p>
  <div class="meta-row">
    <span class="meta-left">
      {#if showRepo}
        <Chip
          size="xs"
          uppercase={false}
          title={repoLabel}
          tone="muted" class="repo-chip"
          style={`color: ${hashColor(repoColorKey)}; background: color-mix(in srgb, ${hashColor(repoColorKey)} 15%, transparent);`}
        >{repoLabel}</Chip>
      {/if}
      <span class="meta-text">#{issue.Number} · {issue.Author}</span>
    </span>
```

Keep `<span class="meta-right">` onward unchanged.

Then apply these style edits in the same file's `<style>` block:

Replace the `.issue-item` padding line:
```css
    padding: 6px 10px;
```

Replace the `.title` rule and add `.title-text`:
```css
  .title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: var(--font-size-md);
    font-weight: 500;
    color: var(--text-primary);
    overflow: hidden;
    margin-bottom: 2px;
  }

  .title-text {
    flex: 0 1 auto;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
```

Replace the `.meta-left` rule and add `.meta-text`:
```css
  .meta-left {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .meta-text {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
```

Delete the `.repo-row` rule. Replace the `:global(.kit-chip.repo-chip)` rule:
```css
  :global(.kit-chip.repo-chip) {
    flex: 0 4 auto;
    justify-content: flex-start;
    min-width: 0;
    max-width: 45%;
    overflow: hidden;
  }
```

Mobile overrides:
- In `:global(.mobile-main) .issue-item`, change the min-height multiplier from `* 1.65` to `* 1.95` (the removed repo/label rows made rows shorter; ~72px keeps mobile rows comfortably tappable and keeps the existing mobile e2e readability assertion meaningful).
- In `:global(.mobile-main) .title`, remove the `white-space: normal`, `display: -webkit-box`, `-webkit-box-orient`, `-webkit-line-clamp`, and `line-clamp` declarations (keep margin, font-size, line-height) and add:
```css
  :global(.mobile-main) .title-text {
    white-space: normal;
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
  }
```
- Delete the `:global(.mobile-main) .repo-row` rule.
- In the `:global(.mobile-main) .meta-left, :global(.mobile-main) .time` font-size rule, replace `.meta-left` with `.meta-text`.

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/sidebar/IssueItem.test.ts
```
Expected: all 4 tests PASS.

- [ ] **Step 5: Commit**

Invoke `context-sync --commit`, then:
```bash
git add packages/ui/src/components/sidebar/IssueItem.svelte packages/ui/src/components/sidebar/IssueItem.test.ts
git commit -m "feat: compact issue sidebar cards to a two-line layout"
```

---

### Task 4: Playwright geometry coverage

**Files:**
- Modify: `frontend/tests/e2e-full/pull-list.spec.ts` (the `"sidebar status pills use the shared chip component"` test, ~line 253)
- Modify: `frontend/tests/e2e-full/issue-list.spec.ts` (the `"sidebar issue pills use the shared chip component"` test, ~line 101)
- Check only (no expected change): `frontend/tests/e2e-full/mobile-routes.spec.ts`

**Interfaces:**
- Consumes: the DOM contract from Tasks 2–3 (`.meta-row .repo-chip`, no `.repo-row`, uniform row heights).
- Produces: nothing downstream.

- [ ] **Step 1: Extend the pull-list geometry assertions**

In `pull-list.spec.ts`, inside `"sidebar status pills use the shared chip component"`, after the existing `await expect(firstItem.locator(".status-chip")).toBeVisible();` line, add:

```ts
    // Compact layout: repo chip lives in the meta row, no standalone repo
    // row, and rows keep a uniform two-line height regardless of labels.
    await expect(firstItem.locator(".meta-row .repo-chip")).toBeVisible();
    await expect(page.locator(".pull-item .repo-row")).toHaveCount(0);
    const rowHeights = await page
      .locator(".pull-item")
      .evaluateAll((nodes) => nodes.slice(0, 6).map((node) => node.getBoundingClientRect().height));
    expect(rowHeights.length).toBeGreaterThan(1);
    for (const height of rowHeights) {
      expect(height).toBeLessThanOrEqual(60);
      expect(Math.abs(height - (rowHeights[0] ?? 0))).toBeLessThanOrEqual(1);
    }
```

Note: `expectRepoChipToClipSafely` (narrowing the item to 180px and asserting the chip clips inside the row with an ellipsis and full `title` attribute) already covers the truncation-order requirement — the chip's new `max-width: 45%` makes it clip first. Leave that helper and its call sites untouched.

- [ ] **Step 2: Extend the issue-list geometry assertions**

In `issue-list.spec.ts`, inside `"sidebar issue pills use the shared chip component"`, after `await expect(firstItem.locator(".state-chip")).toBeVisible();`, add the same block with `.issue-item` selectors:

```ts
      await expect(firstItem.locator(".meta-row .repo-chip")).toBeVisible();
      await expect(page.locator(".issue-item .repo-row")).toHaveCount(0);
      const rowHeights = await page
        .locator(".issue-item")
        .evaluateAll((nodes) => nodes.slice(0, 6).map((node) => node.getBoundingClientRect().height));
      expect(rowHeights.length).toBeGreaterThan(1);
      for (const height of rowHeights) {
        expect(height).toBeLessThanOrEqual(60);
        expect(Math.abs(height - (rowHeights[0] ?? 0))).toBeLessThanOrEqual(1);
      }
```

- [ ] **Step 3: Build and run the affected e2e specs**

```bash
make frontend
GOFLAGS="-buildvcs=false" go build -o ./cmd/e2e-server/e2e-server ./cmd/e2e-server
cd frontend && node ./scripts/run-e2e-to-file.ts pull-list.spec.ts issue-list.spec.ts mobile-routes.spec.ts --project=chromium
```
Output lands in `tmp/e2e.log` (repo root). Expected: all three specs PASS. `mobile-routes.spec.ts` needs no edits — its `.repo-chip` selectors don't reference `.repo-row`, and `expectReadableFocusList`'s `itemRect.height >= 72` assertion is satisfied by the `* 1.95` min-height floor from Tasks 2–3. If it fails on height, fix the component min-height, not the assertion.

- [ ] **Step 4: Commit**

Invoke `context-sync --commit`, then:
```bash
git add frontend/tests/e2e-full/pull-list.spec.ts frontend/tests/e2e-full/issue-list.spec.ts
git commit -m "test: assert compact sidebar rows keep uniform two-line geometry"
```

---

### Task 5: Full validation

**Files:** none created; runs the full local gates required before any push of frontend changes.

**Interfaces:**
- Consumes: all prior tasks committed.
- Produces: a validated branch ready for screenshot capture and PR.

- [ ] **Step 1: Full Vitest run (unit + browser projects)**

```bash
cd frontend && node ../node_modules/vite-plus/bin/vp test run
```
Expected: PASS (runs both the unit and browser projects). This is required by repo policy before pushing any frontend change.

- [ ] **Step 2: Type check and lint**

```bash
node node_modules/vite-plus/bin/vp run frontend-check
```
Expected: clean exit (runs svelte-check/tsc and lint per workspace config). Fix any findings and re-run.

- [ ] **Step 3: Full Playwright e2e suite**

The change touches Playwright specs, so the full affected e2e suite must run locally:
```bash
make test-e2e
```
Expected: PASS. Sidebar rows appear in many specs (focus-mode, grouping-toggle, sidebar-collapse, workspace panels); if an unrelated-looking spec fails on `.title` or row geometry, it is likely this change — fix the component or the stale selector, never skip the spec.

- [ ] **Step 4: Commit any fixes**

If steps 1–3 required fixes, invoke `context-sync --commit` and commit them as separate `fix:`/`test:` commits (never amend).

- [ ] **Step 5: Visual verification**

Use the `capture-playwright` skill to capture the PRs and Issues sidebar (desktop) against the seeded e2e backend for the eventual PR description. Do not upload with `gh image` without explicit user approval; save to local disk and report the paths.
