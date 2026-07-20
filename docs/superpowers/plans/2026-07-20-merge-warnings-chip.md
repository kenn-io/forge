# Merge Warnings Chip Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the layout-shifting merge-warnings banner in the PR detail header with a compact clickable chip that expands a warning panel, following the existing CIStatus/StackStatus pattern.

**Architecture:** A new presentation-only `MergeWarningsChip.svelte` (kit-ui `Chip` + full-width collapse panel) lives in the `chips-row`; `PullDetail.svelte` derives typed warning entries and owns stale filtering plus the single-open panel state. A new shared `providerDisplayLabel` helper in `packages/ui` supplies the provider-aware link label.

**Tech Stack:** Svelte 5 (runes), kit-ui `Chip`, Vitest + @testing-library/svelte (jsdom), Playwright (migration only), Bun + Vite+ (`vp`).

**Spec:** `docs/superpowers/specs/2026-07-20-merge-warnings-chip-design.md`

## Global Constraints

- Never use npm. Frontend deps via `bun install`; invoke Vite+ as `./node_modules/.bin/vp ...` (or `../node_modules/.bin/vp ...` from `frontend/`).
- When creating or editing any `.svelte` file, use the `svelte-code-writer` skill (and `svelte-core-bestpractices`); Svelte 5 runes only.
- Before every git commit, invoke the repo-local `context-sync` skill with `--commit`. Never `--amend`, never bypass pre-commit hooks. The current branch is `feat/merge-warnings-chip`; do not create or switch branches.
- Commit subjects: conventional, imperative, explain the user-visible outcome; bodies add motivating context.
- Warning texts are copied verbatim and must not change:
  - `This branch has conflicts that must be resolved before merging.`
  - `Branch protection rules may prevent this merge.`
  - `This branch is behind the base branch and may need to be updated.`
  - `Required status checks have not passed.`
- No emojis; generic synthetic examples in tests.
- If test runs hang or runners misbehave, use the `local-test-runner-debugging` skill.

---

### Task 1: `providerDisplayLabel` shared helper

**Files:**
- Create: `packages/ui/src/api/provider-labels.ts`
- Create: `packages/ui/src/api/provider-labels.test.ts`
- Modify: `packages/ui/package.json` (exports map, after the `./api/provider-routes` entry)
- Modify: `frontend/src/lib/components/settings/repoImportProviders.ts`

**Interfaces:**
- Consumes: `canonicalProvider(provider: string): string` from `packages/ui/src/api/provider-routes.ts` (already exists).
- Produces: `providerDisplayLabel(provider: string): string` — maps `github → GitHub`, `gitlab → GitLab`, `forgejo → Forgejo`, `gitea → Gitea` (shorthand keys `gh`/`gl`/`fj` canonicalize first); unknown keys return unchanged. Task 3 imports it from `../../api/provider-labels.js`; frontend imports it as `@middleman/ui/api/provider-labels`.

- [ ] **Step 1: Write the failing test**

Create `packages/ui/src/api/provider-labels.test.ts`:

```ts
import { describe, expect, it } from "vite-plus/test";
import { providerDisplayLabel } from "./provider-labels.js";

describe("providerDisplayLabel", () => {
  it("maps known provider keys to display labels", () => {
    expect(providerDisplayLabel("github")).toBe("GitHub");
    expect(providerDisplayLabel("gitlab")).toBe("GitLab");
    expect(providerDisplayLabel("forgejo")).toBe("Forgejo");
    expect(providerDisplayLabel("gitea")).toBe("Gitea");
  });

  it("canonicalizes shorthand and mixed-case keys", () => {
    expect(providerDisplayLabel("gh")).toBe("GitHub");
    expect(providerDisplayLabel("GL")).toBe("GitLab");
    expect(providerDisplayLabel("fj")).toBe("Forgejo");
  });

  it("falls back to the raw key for unknown providers", () => {
    expect(providerDisplayLabel("sourcehut")).toBe("sourcehut");
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./node_modules/.bin/vp test run --project unit provider-labels`
Expected: FAIL — cannot resolve `./provider-labels.js`.

- [ ] **Step 3: Write the implementation**

Create `packages/ui/src/api/provider-labels.ts`:

```ts
import { canonicalProvider } from "./provider-routes.js";

const displayLabels: Record<string, string> = {
  github: "GitHub",
  gitlab: "GitLab",
  forgejo: "Forgejo",
  gitea: "Gitea",
};

// providerDisplayLabel maps a provider key to its user-facing label.
// Unknown providers fall back to the raw key so UI copy still renders.
export function providerDisplayLabel(provider: string): string {
  return displayLabels[canonicalProvider(provider)] ?? provider;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./node_modules/.bin/vp test run --project unit provider-labels`
Expected: PASS (3 tests).

- [ ] **Step 5: Export the module from `@middleman/ui`**

In `packages/ui/package.json`, add after the `"./api/provider-routes"` entry:

```json
"./api/provider-labels": {
  "default": "./src/api/provider-labels.ts"
},
```

- [ ] **Step 6: Wire `repoImportProviders.ts` labels through the helper**

In `frontend/src/lib/components/settings/repoImportProviders.ts`, add the import and replace the four hardcoded `label:` values so the two tables cannot drift:

```ts
import { providerDisplayLabel } from "@middleman/ui/api/provider-labels";
```

and in each entry: `label: providerDisplayLabel("github")`, `label: providerDisplayLabel("gitlab")`, `label: providerDisplayLabel("forgejo")`, `label: providerDisplayLabel("gitea")`. No other fields change.

- [ ] **Step 7: Run frontend unit tests and typecheck**

Run: `cd frontend && bun run test && bun run typecheck`
Expected: PASS, 0 type errors.

- [ ] **Step 8: Commit**

Invoke the `context-sync` skill with `--commit`, then:

```bash
git add packages/ui/src/api/provider-labels.ts packages/ui/src/api/provider-labels.test.ts packages/ui/package.json frontend/src/lib/components/settings/repoImportProviders.ts
git commit -m "feat: add shared provider display-label helper"
```

Body: explain that no provider display label existed in shared TypeScript (labels lived only in Go metadata and a frontend-only import table), and this helper backs the provider-aware link in the upcoming merge warnings chip.

---

### Task 2: `MergeWarningsChip` component

**Files:**
- Create: `packages/ui/src/components/detail/MergeWarningsChip.svelte`
- Create: `packages/ui/src/components/detail/MergeWarningsChip.test.ts`

**Interfaces:**
- Consumes: `Chip` from `@kenn-io/kit-ui`; lucide icons via deep imports.
- Produces: default export component with props `{ warnings: MergeWarningEntry[]; pullURL: string; providerLabel: string; expanded?: boolean; ontoggle?: (expanded: boolean) => void }` and module-script export `type MergeWarningEntry = { kind: "conflict" | "blocked" | "behind" | "required-checks" | "server"; text: string }`. Renders nothing when `warnings` is empty. Chip testid: `merge-warnings-chip`. Task 3 and Task 4 rely on that testid.

- [ ] **Step 1: Write the failing tests**

Create `packages/ui/src/components/detail/MergeWarningsChip.test.ts`:

```ts
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import MergeWarningsChip, { type MergeWarningEntry } from "./MergeWarningsChip.svelte";

const conflictEntry: MergeWarningEntry = {
  kind: "conflict",
  text: "This branch has conflicts that must be resolved before merging.",
};
const behindEntry: MergeWarningEntry = {
  kind: "behind",
  text: "This branch is behind the base branch and may need to be updated.",
};

function renderChip(warnings: MergeWarningEntry[], ontoggle = vi.fn()) {
  render(MergeWarningsChip, {
    props: {
      warnings,
      pullURL: "https://gitlab.com/acme/widget/-/merge_requests/7",
      providerLabel: "GitLab",
      ontoggle,
    },
  });
  return ontoggle;
}

describe("MergeWarningsChip", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders nothing when there are no warnings", () => {
    renderChip([]);
    expect(screen.queryByTestId("merge-warnings-chip")).toBeNull();
  });

  it("shows the Conflicts label with warning tone when a conflict entry exists", () => {
    renderChip([conflictEntry, behindEntry]);
    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("Conflicts");
    expect(chip.className).toContain("kit-chip--tone-warning");
  });

  it("shows a singular count with neutral tone for one non-conflict warning", () => {
    renderChip([behindEntry]);
    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("1 merge warning");
    expect(chip.className).toContain("kit-chip--tone-neutral");
  });

  it("pluralizes the count for multiple non-conflict warnings", () => {
    renderChip([behindEntry, { kind: "server", text: "Example sync warning" }]);
    expect(screen.getByTestId("merge-warnings-chip").textContent).toContain("2 merge warnings");
  });

  it("toggles the panel and reports through ontoggle", async () => {
    const ontoggle = renderChip([conflictEntry]);
    expect(screen.queryByText(conflictEntry.text)).toBeNull();

    await fireEvent.click(screen.getByTestId("merge-warnings-chip"));
    expect(ontoggle).toHaveBeenLastCalledWith(true);
    expect(screen.getByText(conflictEntry.text)).toBeTruthy();

    await fireEvent.click(screen.getByTestId("merge-warnings-chip"));
    expect(ontoggle).toHaveBeenLastCalledWith(false);
    expect(screen.queryByText(conflictEntry.text)).toBeNull();
  });

  it("lists entries in the given order with a provider link", async () => {
    renderChip([conflictEntry, behindEntry]);
    await fireEvent.click(screen.getByTestId("merge-warnings-chip"));

    const lines = Array.from(document.querySelectorAll(".merge-warning-line")).map((line) =>
      line.textContent?.trim(),
    );
    expect(lines).toEqual([conflictEntry.text, behindEntry.text]);

    const link = screen.getByRole("link", { name: "View on GitLab" });
    expect(link.getAttribute("href")).toBe("https://gitlab.com/acme/widget/-/merge_requests/7");
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `./node_modules/.bin/vp test run --project unit MergeWarningsChip`
Expected: FAIL — cannot resolve `./MergeWarningsChip.svelte`.

- [ ] **Step 3: Write the component** (use the `svelte-code-writer` skill)

Create `packages/ui/src/components/detail/MergeWarningsChip.svelte`:

```svelte
<script module lang="ts">
  export type MergeWarningEntry = {
    kind: "conflict" | "blocked" | "behind" | "required-checks" | "server";
    text: string;
  };
</script>

<script lang="ts">
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import GitMergeIcon from "@lucide/svelte/icons/git-merge";
  import { Chip } from "@kenn-io/kit-ui";

  interface Props {
    warnings: MergeWarningEntry[];
    pullURL: string;
    providerLabel: string;
    expanded?: boolean;
    ontoggle?: ((expanded: boolean) => void) | undefined;
  }

  let {
    warnings,
    pullURL,
    providerLabel,
    expanded = $bindable(false),
    ontoggle,
  }: Props = $props();

  const hasConflict = $derived(warnings.some((warning) => warning.kind === "conflict"));
  const countLabel = $derived(
    `${warnings.length} merge warning${warnings.length === 1 ? "" : "s"}`,
  );
  const chipLabel = $derived(hasConflict ? "Conflicts" : countLabel);
  const chipAriaLabel = $derived(hasConflict ? `Merge conflicts, ${countLabel}` : countLabel);

  function toggleExpanded(): void {
    const next = !expanded;
    expanded = next;
    ontoggle?.(next);
  }
</script>

{#if warnings.length > 0}
  <div class="merge-warnings-status">
    <Chip size="sm"
      interactive={true}
      tone={hasConflict ? "warning" : "neutral"}
      uppercase={false}
      ariaLabel={chipAriaLabel}
      dataTestid="merge-warnings-chip"
      onclick={toggleExpanded}
      title={expanded ? "Collapse merge warnings" : "Expand merge warnings"}
      {expanded}
    >
      <GitMergeIcon size={12} strokeWidth={2.3} aria-hidden="true" />
      <span>{chipLabel}</span>
      {#snippet trailing()}
        <ChevronDownIcon
          class={["chip-chevron", expanded && "chip-chevron--open"].filter(Boolean).join(" ")}
          size={12}
          strokeWidth={2.4}
          aria-hidden="true"
        />
      {/snippet}
    </Chip>

    {#if expanded}
      <div class="merge-warnings-collapse">
        <div class="merge-warnings-panel" aria-label="Merge warnings">
          {#each warnings as warning, index (`${index}-${warning.kind}`)}
            <div
              class="merge-warning-line"
              class:merge-warning-line--conflict={warning.kind === "conflict"}
            >
              <span>{warning.text}</span>
            </div>
          {/each}
          <a
            class="merge-warnings-link"
            href={pullURL}
            target="_blank"
            rel="noopener noreferrer"
          >View on {providerLabel}</a>
        </div>
      </div>
    {/if}
  </div>
{/if}

<style>
  .merge-warnings-status {
    display: contents;
  }

  :global(.chip-chevron) {
    flex-shrink: 0;
    vertical-align: middle;
    transition: transform 0.15s;
  }

  :global(.chip-chevron--open) {
    transform: rotate(180deg);
  }

  /* Full-width row in the wrapping chips-row: order pushes the panel
   * below every chip, matching StackStatus's collapse behavior. */
  .merge-warnings-collapse {
    order: 999;
    flex-basis: 100%;
    width: 100%;
    min-width: 0;
    margin-top: 4px;
  }

  .merge-warnings-panel {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px 12px;
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .merge-warning-line--conflict {
    color: var(--accent-amber);
  }

  .merge-warnings-link {
    color: inherit;
    text-decoration: underline;
    align-self: flex-start;
  }
</style>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./node_modules/.bin/vp test run --project unit MergeWarningsChip`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

Invoke the `context-sync` skill with `--commit`, then:

```bash
git add packages/ui/src/components/detail/MergeWarningsChip.svelte packages/ui/src/components/detail/MergeWarningsChip.test.ts
git commit -m "feat: add merge warnings chip component"
```

Body: note this is the chip-and-panel replacement for the merge-warnings banner, not yet wired into PullDetail.

---

### Task 3: Integrate into PullDetail, remove the banner

**Files:**
- Modify: `packages/ui/src/components/detail/PullDetail.svelte`
- Modify: `packages/ui/src/components/detail/PullDetail.test.ts`

**Interfaces:**
- Consumes: `MergeWarningsChip` + `MergeWarningEntry` (Task 2), `providerDisplayLabel` (Task 1).
- Produces: `merge-warnings-chip` testid reachable from a rendered PullDetail; `expandedPanel` accepts `"merge"`. The banner texts no longer render without expanding the chip (Task 4 depends on this).

- [ ] **Step 1: Migrate the existing warning tests and add the new integration tests (failing first)**

In `packages/ui/src/components/detail/PullDetail.test.ts`, replace the `warningCases` array and its `for` loop (the block containing `"does not describe GitHub unstable mergeability as required checks"` and `"shows required CI and branch freshness warnings independently"`) with:

```ts
  it("does not describe GitHub unstable mergeability as required checks", () => {
    const detail = pullDetail();
    detail.merge_request.MergeableState = "unstable";
    detail.merge_request.CIStatus = "failure";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      {
        name: "e2e",
        status: "completed",
        conclusion: "failure",
        url: "https://example.com/e2e",
        app: "GitHub Actions",
      },
    ]);

    renderPullDetail(detail);

    expect(screen.queryByTestId("merge-warnings-chip")).toBeNull();
  });

  it("shows required CI and branch freshness warnings behind the merge warnings chip", async () => {
    const detail = pullDetail();
    detail.merge_request.MergeableState = "behind";
    detail.merge_request.CIStatus = "failure";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      {
        name: "build",
        status: "completed",
        conclusion: "failure",
        url: "https://example.com/build",
        app: "GitHub Actions",
        required: true,
      },
    ]);

    renderPullDetail(detail);

    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("2 merge warnings");
    expect(screen.queryByText("Required status checks have not passed.")).toBeNull();

    await fireEvent.click(chip);

    expect(screen.getByText("Required status checks have not passed.")).toBeTruthy();
    expect(
      screen.getByText("This branch is behind the base branch and may need to be updated."),
    ).toBeTruthy();
  });

  it("labels the chip Conflicts and links to the provider when the branch is dirty", async () => {
    const detail = pullDetail();
    detail.merge_request.MergeableState = "dirty";

    renderPullDetail(detail);

    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("Conflicts");

    await fireEvent.click(chip);

    expect(
      screen.getByText("This branch has conflicts that must be resolved before merging."),
    ).toBeTruthy();
    const link = screen.getByRole("link", { name: "View on GitHub" });
    expect(link.getAttribute("href")).toBe(detail.merge_request.URL);
  });

  it("shows only server warnings on the chip when the detail is stale", async () => {
    const detail = pullDetail();
    detail.repo_owner = "someone-else";
    detail.merge_request.MergeableState = "dirty";
    detail.warnings = ["Example sync warning"];

    renderPullDetail(detail);

    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("1 merge warning");

    await fireEvent.click(chip);

    expect(screen.getByText("Example sync warning")).toBeTruthy();
    expect(
      screen.queryByText("This branch has conflicts that must be resolved before merging."),
    ).toBeNull();
  });
```

Then extend the existing `"uses one shared expanded slot for CI and stack status"` test: inside it, after the stack assertions, add a dirty mergeable state at the top (`detail.merge_request.MergeableState = "dirty";` right after `detail.merge_request.Number = 2;`) and append at the end:

```ts
    await fireEvent.click(screen.getByTestId("merge-warnings-chip"));

    expect(screen.queryByText("3 PRs · current 2/3 · downstack CI failure")).toBeNull();
    expect(
      screen.getByText("This branch has conflicts that must be resolved before merging."),
    ).toBeTruthy();
```

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `./node_modules/.bin/vp test run --project unit packages/ui/src/components/detail/PullDetail.test.ts`
Expected: the four new/migrated tests and the extended shared-slot test FAIL (no `merge-warnings-chip` testid); pre-existing tests still pass.

- [ ] **Step 3: Wire the chip into PullDetail** (use the `svelte-code-writer` skill)

In `packages/ui/src/components/detail/PullDetail.svelte`:

a. Widen the panel state (line ~195):

```ts
  let expandedPanel = $state<"ci" | "stack" | "merge" | null>(null);
```

b. Add imports next to the other detail-component imports:

```ts
  import MergeWarningsChip, { type MergeWarningEntry } from "./MergeWarningsChip.svelte";
  import { providerDisplayLabel } from "../../api/provider-labels.js";
```

c. Replace the `hasWarningLines` function (lines ~263-281) with the entry derivation (keep `requiredStatusChecksHaveNotPassed` unchanged):

```ts
  function mergeWarningEntries(
    pr: Pick<PullRequest, "State" | "MergeableState" | "CIChecksJSON">,
    warnings: readonly string[] | null | undefined,
    stale: boolean,
  ): MergeWarningEntry[] {
    const entries: MergeWarningEntry[] = [];
    if (!stale && pr.State === "open") {
      if (pr.MergeableState === "dirty") {
        entries.push({
          kind: "conflict",
          text: "This branch has conflicts that must be resolved before merging.",
        });
      }
      if (pr.MergeableState === "blocked") {
        entries.push({
          kind: "blocked",
          text: "Branch protection rules may prevent this merge.",
        });
      }
      if (pr.MergeableState === "behind") {
        entries.push({
          kind: "behind",
          text: "This branch is behind the base branch and may need to be updated.",
        });
      }
      if (requiredStatusChecksHaveNotPassed(pr.CIChecksJSON)) {
        entries.push({
          kind: "required-checks",
          text: "Required status checks have not passed.",
        });
      }
    }
    for (const warning of warnings ?? []) {
      entries.push({ kind: "server", text: warning });
    }
    return entries;
  }
```

d. In the `chips-row`, immediately after the first `<CIStatus ... showPanel={false} />` instance (line ~1831), add:

```svelte
        <MergeWarningsChip
          warnings={mergeWarningEntries(pr, detail.warnings, stalePR)}
          pullURL={pr.URL}
          providerLabel={providerDisplayLabel(detail.repo?.provider ?? provider)}
          expanded={expandedPanel === "merge"}
          ontoggle={(next) => { expandedPanel = next ? "merge" : null; }}
        />
```

e. Delete the entire banner block — both branches of the `{#if !stalePR && hasWarningLines(...)}` / `{:else if stalePR && detail.warnings ...}` conditional (the `<!-- Pull request warnings -->` comment through its closing `{/if}`, lines ~1956-1996).

f. Delete the now-unused styles: `.merge-warnings`, `.merge-warning-line`, `.merge-warning-line a`, `.merge-warning-line--conflict` (lines ~3201-3228).

- [ ] **Step 4: Run the PullDetail tests**

Run: `./node_modules/.bin/vp test run --project unit packages/ui/src/components/detail/PullDetail.test.ts`
Expected: PASS, including the four new tests and the extended shared-slot test.

- [ ] **Step 5: Run the full unit project and typecheck**

Run: `./node_modules/.bin/vp test run --project unit && cd frontend && bun run typecheck && bun run check`
Expected: PASS / 0 errors. (Unrelated failures: stop and report, do not fix.)

- [ ] **Step 6: Commit**

Invoke the `context-sync` skill with `--commit`, then:

```bash
git add packages/ui/src/components/detail/PullDetail.svelte packages/ui/src/components/detail/PullDetail.test.ts
git commit -m "feat: replace merge warnings banner with expandable chip"
```

Body: the banner appeared below the kanban select as sync data arrived and reflowed the detail view; warnings now live in a chip beside the CI chip using the shared expanded-panel slot, and the view link is provider-aware instead of hardcoded to GitHub.

---

### Task 4: Migrate Playwright and browser tests off the banner

**Files:**
- Modify: `frontend/tests/e2e/detail-stale-actions.spec.ts`
- Modify: `frontend/src/App.stack-status.browser.svelte.ts`

**Interfaces:**
- Consumes: `merge-warnings-chip` testid (Tasks 2-3).
- Produces: no test anywhere asserts banner text visible without expanding the chip.

- [ ] **Step 1: Update the conflict merge-button e2e test**

In `frontend/tests/e2e/detail-stale-actions.spec.ts`, in the test `"merge button is disabled when the PR has merge conflicts"`, replace:

```ts
    await expect(page.getByText("This branch has conflicts")).toBeVisible();
```

with:

```ts
    await expect(page.getByTestId("merge-warnings-chip")).toContainText("Conflicts");
```

- [ ] **Step 2: Remove the duplicated warning-line e2e cases**

In the same file, delete the entire `warningLineCases` array and its `for (const { name, pr, requiredWarning, behindWarning } of warningLineCases) { ... }` loop. These asserted backend-independent warning-line presentation already covered by the jsdom tests migrated in Task 3; per the repo testing rules, duplicate full-stack e2e for UI-owned presentation is not kept.

- [ ] **Step 3: Update the browser test readiness signal**

In `frontend/src/App.stack-status.browser.svelte.ts` (line ~206), replace:

```ts
    await expect
      .element(page.getByText("This branch has conflicts that must be resolved before merging."))
      .toBeVisible();
```

with:

```ts
    await expect
      .element(page.getByTestId("merge-warnings-chip"))
      .toBeVisible();
```

- [ ] **Step 4: Verify no other test references the banner texts**

Run: `rg -n "has conflicts that must be resolved|Required status checks have not passed|behind the base branch|Branch protection rules" frontend/ packages/ --glob '!node_modules'`
Expected: matches only in `PullDetail.svelte`, `MergeWarningsChip.*`, and `PullDetail.test.ts` — no Playwright/browser files.

- [ ] **Step 5: Run the migrated suites**

Run: `cd frontend && bun run test:browser && node ../node_modules/vite-plus/bin/vp exec -- playwright test tests/e2e/detail-stale-actions.spec.ts --config=playwright.config.ts`
Expected: PASS.

- [ ] **Step 6: Commit**

Invoke the `context-sync` skill with `--commit`, then:

```bash
git add frontend/tests/e2e/detail-stale-actions.spec.ts frontend/src/App.stack-status.browser.svelte.ts
git commit -m "test: migrate warning assertions to the merge warnings chip"
```

Body: the banner text these tests awaited no longer renders unexpanded; the chip is the new signal, and the warning-line e2e cases were dropped as duplicates of the jsdom coverage.

---

### Task 5: Full verification

**Files:** none created; runs the pre-push validation required by the repo before any frontend push.

- [ ] **Step 1: Full Vitest run (all projects)**

Run: `./node_modules/.bin/vp test run`
Expected: PASS. If the browser project is not included in the default run, additionally run `cd frontend && bun run test:browser`.

- [ ] **Step 2: Full affected Playwright suite**

Playwright specs were touched, so run the full mock e2e suite:

Run: `cd frontend && bun run test:e2e:mock`
Expected: PASS.

- [ ] **Step 3: Lint and typecheck**

Run: `cd frontend && bun run lint && bun run typecheck && bun run check`
Expected: 0 errors, 0 warnings.

- [ ] **Step 4: Report**

No commit if nothing changed. Report results; fixes for failures discovered here get their own commits (never `--amend`). PR creation, screenshot capture (`capture-playwright` skill), and the scrub-private-data pass happen after the user reviews the completed branch — not part of this plan.
