# Collapsible Long Descriptions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an expanded-by-default collapse control to provider PR and issue descriptions whose Markdown source exceeds 1,500 characters.

**Approved spec/design:** `docs/superpowers/specs/2026-07-29-collapsible-long-descriptions-design.md`

**Architecture:** Introduce one shared `CollapsibleDescription` Svelte component that owns threshold detection, transient collapse state, the description header, copy control, and compact card styling. Pull and issue detail components retain their existing Markdown rendering and interaction handlers, key the shared component by normalized provider-aware identity, and pass rendered content as a snippet.

**Tech Stack:** Svelte 5 runes and snippets, TypeScript, `@kenn-io/kit-ui`, Testing Library, Vite+ unit and browser projects.

## Global Constraints

- Provider PR and issue descriptions only; Kata task descriptions remain unchanged.
- A description is long only when its Markdown source length is greater than 1,500 characters.
- Long descriptions start expanded; compact descriptions have a 320px maximum height and vertical scrolling.
- Collapse state is transient, per item, and resets when the full provider-aware item identity changes.
- Existing edit, copy, Markdown, task-list, and drag behavior must remain intact.
- Do not add persistence, API changes, or dependencies.

---

### Task 1: Shared collapse behavior in pull request details

**Files:**
- Create: `packages/ui/src/components/detail/CollapsibleDescription.svelte`
- Modify: `packages/ui/src/components/detail/PullDetail.svelte:2714-2802`
- Test: `packages/ui/src/components/detail/PullDetail.test.ts`

**Interfaces:**
- Produces: `CollapsibleDescription` with props `source: string`, `copied: boolean`, `oncopy: () => void`, optional `headerActions?: Snippet`, and `children: Snippet`.
- Produces: visible `Collapse` / `Expand` control with accessible names `Collapse description` / `Expand description` and `aria-expanded` state.
- Consumes: the existing `CopyButton`, `Card`, PR body copy callback, edit callback, Markdown renderer, and body drag handlers.

- [ ] **Step 1: Write the failing expanded-default test**

Add this focused behavior test near `PullDetail body copy feedback`:

```ts
it("offers an expanded collapse control for a long pull description", () => {
  const detail = pullDetail();
  detail.merge_request.Body = "x".repeat(1_501);
  renderPullDetail(detail);

  const collapse = screen.getByRole("button", { name: "Collapse description" });
  expect(collapse.getAttribute("aria-expanded")).toBe("true");
});
```

The production mutation caught by this test is omitting the long-description control or defaulting it to compact.

- [ ] **Step 2: Run the test and verify the missing behavior**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/detail/PullDetail.test.ts -t "offers an expanded collapse control"
```

Expected: FAIL because no button named `Collapse description` exists.

- [ ] **Step 3: Add the minimal shared component and use it for PR bodies**

Create the component with this public shape and initial state:

```svelte
<script lang="ts">
  import { Card, CopyButton } from "@kenn-io/kit-ui";
  import type { Snippet } from "svelte";

  interface Props {
    source: string;
    copied: boolean;
    oncopy: () => void;
    headerActions?: Snippet;
    children: Snippet;
  }

  const { source, copied, oncopy, headerActions, children }: Props = $props();
  let collapsed = $state(false);
  const isLong = true;

  function toggleCollapsed(): void {
    collapsed = !collapsed;
  }
</script>
```

Render the `Description` header, the conditional text button, the existing copy button contract, a `Card level="inset" padding="none"`, and `{@render children()}`. The toggle uses `aria-expanded={!collapsed}`, the visible label is `Collapse` or `Expand`, and its `aria-label` adds `description`.

In `PullDetail.svelte`, replace only the non-editing `pr.Body` header/card branch with `CollapsibleDescription`. Derive a normalized provider-aware key containing `canonicalProvider(provider)`, `resolvedPlatformHost(provider, platformHost)`, `owner`, `name`, `number`, and the `pull` kind, then wrap the shared component in a Svelte `{#key}` block. Pass the existing Edit button as `headerActions`; keep the textarea/add-description branches in `PullDetail`.

- [ ] **Step 4: Run the focused test and verify it passes**

Run the Step 2 command.

Expected: PASS.

- [ ] **Step 5: Write failing threshold and identity-reset tests**

Add two tests:

```ts
it("does not offer collapse at the long-description threshold", () => {
  const detail = pullDetail();
  detail.merge_request.Body = "x".repeat(1_500);
  renderPullDetail(detail);

  expect(screen.queryByRole("button", { name: "Collapse description" })).toBeNull();
});

it("expands a collapsed description after pull navigation", async () => {
  const detail = pullDetail();
  detail.merge_request.Body = "x".repeat(1_501);
  const { rerender } = renderPullDetail(detail);

  await fireEvent.click(screen.getByRole("button", { name: "Collapse description" }));
  expect(screen.getByRole("button", { name: "Expand description" }).getAttribute("aria-expanded")).toBe("false");

  detail.merge_request.Number = 2;
  await rerender({ number: 2 });

  expect(screen.getByRole("button", { name: "Collapse description" }).getAttribute("aria-expanded")).toBe("true");

  detail.merge_request.Number = 1;
  await rerender({ number: 1 });

  expect(screen.getByRole("button", { name: "Collapse description" }).getAttribute("aria-expanded")).toBe("true");
});
```

The first test catches an off-by-one threshold. The second catches collapse state leaking between provider item identities.

- [ ] **Step 6: Run the new tests and verify the expected failures**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/detail/PullDetail.test.ts -t "long-description threshold|after pull navigation"
```

Expected: both tests FAIL because the minimal implementation shows the control unconditionally and stores collapse as a plain boolean.

- [ ] **Step 7: Complete threshold and per-item state behavior**

Replace the unconditional threshold with the final source-length derivation:

```ts
let collapsed = $state(false);
const isLong = $derived(source.length > 1_500);

function toggleCollapsed(): void {
  collapsed = !collapsed;
}
```

In each provider detail parent, derive `descriptionItemKey` from canonical provider, resolved host, owner, name, item kind, and number. Wrap the complete existing `CollapsibleDescription` call in `{#key descriptionItemKey}` / `{/key}` without changing its rendered Markdown child.

Do not use `$effect` or persistence: Svelte recreates the transient component state whenever navigation changes normalized identity, including an A → B → A round trip.

- [ ] **Step 8: Run the entire PullDetail unit file**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/detail/PullDetail.test.ts
```

Expected: PASS, including existing copy and editing behavior.

- [ ] **Step 9: Commit Task 1**

Before committing, run the repository-local `context-sync` skill with `--commit`, then the mandatory commit skill. Stage only these files and create a conventional commit explaining why long descriptions need an opt-in compact state.

---

### Task 2: Compact scrolling in a real browser

**Files:**
- Modify: `packages/ui/src/components/detail/CollapsibleDescription.svelte`
- Create: `frontend/src/test/CollapsibleDescriptionBrowserFixture.svelte`
- Create: `frontend/src/CollapsibleDescription.browser.svelte.ts`

**Interfaces:**
- Consumes: `CollapsibleDescription` from Task 1.
- Produces: `.detail-description-card--compact` with computed `max-height: 320px` and `overflow-y: auto`.

- [ ] **Step 1: Write the browser fixture and failing layout test**

The fixture renders `CollapsibleDescription` with a 1,501-character source, a stable item key, and content tall enough to overflow:

```svelte
<CollapsibleDescription source={"x".repeat(1_501)} copied={false} oncopy={() => {}}>
  <div style="height: 640px">Long rendered description</div>
</CollapsibleDescription>
```

The browser test mounts the fixture, clicks `Collapse description`, selects `.detail-description-card--compact`, and asserts:

```ts
expect(getComputedStyle(card).maxHeight).toBe("320px");
expect(getComputedStyle(card).overflowY).toBe("auto");
```

The production mutation caught by this test is toggling the label/state without creating the requested bounded scrolling surface.

- [ ] **Step 2: Run the browser test and verify it fails**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project browser src/CollapsibleDescription.browser.svelte.ts
```

Expected: FAIL because the compact card does not yet compute to a 320px scroll container.

- [ ] **Step 3: Add compact card styling**

In `CollapsibleDescription.svelte`, apply the compact modifier only when `collapsed` is true:

```css
:global(.detail-description-card) {
  overflow: hidden;
}

:global(.detail-description-card--compact) {
  max-height: 320px;
  overflow-y: auto;
}
```

Keep the copy button outside the scrolling `Card` so it remains reachable while the description scrolls. Use the existing spacing tokens for new gaps and padding.

- [ ] **Step 4: Run the browser test and verify it passes**

Run the Step 2 command.

Expected: PASS.

- [ ] **Step 5: Validate the new Svelte component**

Run:

```bash
node node_modules/vite-plus/bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/CollapsibleDescription.svelte --svelte-version 5
node node_modules/vite-plus/bin/vp exec -- svelte-check --tsconfig packages/ui/tsconfig.json --fail-on-warnings
```

Expected: no actionable autofixer findings and no Svelte errors or warnings.

- [ ] **Step 6: Commit Task 2**

Before committing, run `context-sync --commit` and the mandatory commit skill. Stage the shared component and browser test/fixture only.

---

### Task 3: Reuse the collapse surface for issue details

**Files:**
- Modify: `packages/ui/src/components/detail/IssueDetail.svelte:1375-1418`
- Test: `packages/ui/src/components/detail/IssueDetail.test.ts`

**Interfaces:**
- Consumes: the `CollapsibleDescription` props and behavior completed in Tasks 1-2.
- Preserves: existing issue body copy callback, rendered Markdown, interactive tasks, and body drag handlers.

- [ ] **Step 1: Write the failing issue integration test**

Add:

```ts
it("collapses and expands a long issue description", async () => {
  const detail = issueDetail();
  detail.issue.Body = "x".repeat(1_501);
  renderIssueDetail(detail);

  await fireEvent.click(screen.getByRole("button", { name: "Collapse description" }));
  const expand = screen.getByRole("button", { name: "Expand description" });
  expect(expand.getAttribute("aria-expanded")).toBe("false");

  await fireEvent.click(expand);
  expect(screen.getByRole("button", { name: "Collapse description" }).getAttribute("aria-expanded")).toBe("true");
});
```

The production mutation caught by this test is wiring the shared behavior only into pull requests.

- [ ] **Step 2: Run the test and verify it fails**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/detail/IssueDetail.test.ts -t "collapses and expands a long issue description"
```

Expected: FAIL because IssueDetail does not yet render the shared control.

- [ ] **Step 3: Replace the issue description wrapper**

Import `CollapsibleDescription` in `IssueDetail.svelte` and replace the existing `section-header` / `inset-box-wrap` / `Card` shell for non-empty issue bodies. Pass `issue.Body` plus the existing copy state and callback, and key the component by the normalized provider/host/owner/name/number identity with the `issue` kind. Keep the existing `.inset-box__content markdown-body` div and every click/drag/drop handler as the child snippet.

- [ ] **Step 4: Run the issue test file and verify it passes**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/detail/IssueDetail.test.ts
```

Expected: PASS.

- [ ] **Step 5: Run final affected verification**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/detail/PullDetail.test.ts ../packages/ui/src/components/detail/IssueDetail.test.ts
cd frontend && ../node_modules/.bin/vp test run --project browser src/CollapsibleDescription.browser.svelte.ts
node node_modules/vite-plus/bin/vp run ui-package-check
```

Expected: all commands pass. Playwright/full-stack testing is intentionally omitted because the interaction is component-owned and the only layout-sensitive contract is covered in the real-browser Vitest test.

- [ ] **Step 6: Commit Task 3**

Run `context-sync --commit`, load the mandatory commit skill, review the complete diff, and commit the issue integration and any final scoped fixes. Do not amend earlier commits.
