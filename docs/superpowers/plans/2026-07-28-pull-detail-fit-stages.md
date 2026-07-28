# Pull Detail Action Fit Stages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace PullDetail's hand-written medium-width Button overrides with Kit UI's measurement-driven `FitStages`.

**Architecture:** PullDetail renders full-label and short-label action stages through `FitStages`. Both stages use Button's normal `label` prop, while the existing 340px Actions-menu fallback handles widths below the compact stage.

**Tech Stack:** Svelte 5, Kit UI Button and FitStages, Playwright

## Global Constraints

- Use the currently pinned `@kenn-io/kit-ui` unchanged.
- Do not style `.kit-button__label`, `.kit-button__short-label`, or Button icons from PullDetail.
- Preserve independent command-button semantics; do not use the radio-group `SegmentedControl`.
- Preserve the existing 340px Actions-menu fallback.

---

### Task 1: Guard adaptive action rendering

**Files:**
- Modify: `frontend/tests/e2e-full/detail-action-buttons.spec.ts`

**Interfaces:**
- Consumes: the pull-detail action row and Kit UI's `.kit-fit-stages` host.
- Produces: a browser regression that detects hand-written Button-internal overrides.

- [ ] **Step 1: Write the failing test**

Load draft pull request `acme/widgets#6`, constrain
`.pull-detail-content` first to a wide width and then to 500px, and assert:

- the action group is hosted by `.kit-fit-stages`;
- wide rendering shows `Ready for review`;
- medium rendering shows the fitting label stage;
- visible labels are normal `.kit-button__label` wrappers centered on their
  buttons;
- Ready, Approve, Merge, and Close icons remain visible.

- [ ] **Step 2: Run the test to verify it fails**

Run:

```bash
cd frontend
../node_modules/.bin/playwright test --config playwright-e2e.config.ts --project chromium tests/e2e-full/detail-action-buttons.spec.ts --grep "fits pull actions without overriding Kit UI"
```

Expected: FAIL because PullDetail does not render `FitStages` and swaps Kit UI
label internals through CSS.

### Task 2: Render measured full and compact stages

**Files:**
- Modify: `packages/ui/src/components/detail/PullDetail.svelte`
- Modify: `packages/ui/src/components/detail/ReadyForReviewButton.svelte`
- Modify: `packages/ui/src/components/detail/ApproveWorkflowsButton.svelte`

**Interfaces:**
- Consumes: `FitStages`, Button's `label` prop, and the existing action handlers.
- Produces: `compactLabel?: boolean` presentation props on the two local action components and a single measured action stage in PullDetail.

- [ ] **Step 1: Add explicit label variants**

Add `compactLabel?: boolean` to `ReadyForReviewButton` and
`ApproveWorkflowsButton`. Select the short or full text before passing it to
Button's `label` prop; remove their `shortLabel` props.

- [ ] **Step 2: Add PullDetail stages**

Parameterize the primary and workspace action snippets with
`compactLabel = false`. Render non-wrapping full and compact primary action
rows through:

```svelte
<FitStages
  bind:stage={primaryActionStage}
  stages={[fullPrimaryActions, compactPrimaryActions]}
/>
```

Use `primaryActionStage` for the separate workspace row's label variant.

- [ ] **Step 3: Remove legacy overrides**

Delete the 560px Close-icon rule, the 520px full/short-label rules, and the
redundant Actions-popover short-label rule. Keep the 340px whole-row/menu
transition.

- [ ] **Step 4: Run focused tests**

Run the browser regression from Task 1 and:

```bash
./node_modules/.bin/vp test packages/ui/src/components/detail/PullDetail.test.ts
./node_modules/.bin/vp run ui-package-typecheck
```

Expected: all commands pass.

- [ ] **Step 5: Validate Svelte**

Run the Svelte autofixer on all three modified components:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/PullDetail.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/ReadyForReviewButton.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/ApproveWorkflowsButton.svelte
```

Expected: no required fixes.

### Task 3: Verify the live pane

**Files:**
- No code changes.

**Interfaces:**
- Consumes: the running ephemeral Middleman frontend.
- Produces: visual confirmation that the actual resizable pane selects a centered action stage.

- [ ] **Step 1: Inspect with Computer Use**

Resize the live pull-detail pane through the wide and medium stages. Confirm
the labels remain vertically centered and the Close icon remains present.

- [ ] **Step 2: Run the required frontend suite**

Run the full Vitest suite and affected full-stack Playwright file after the
final frontend edit, then run `make frontend-check`.
