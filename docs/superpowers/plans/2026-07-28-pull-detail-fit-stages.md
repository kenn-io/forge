# Pull Detail Action Fit Stages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace PullDetail's hand-written medium-width Button overrides with Kit UI's measurement-driven `FitStages`.

**Architecture:** PullDetail gives `FitStages` stateless full-label, compact-label, and Actions-trigger measurement probes. The stateful action controls render once outside the probes and change presentation from the selected stage, so dialog drafts and pending state survive resizing. The measured Actions stage replaces the old fixed-width menu fallback.

**Tech Stack:** Svelte 5, Kit UI `Button` and `FitStages`, Playwright

## Global Constraints

- Use the currently pinned `@kenn-io/kit-ui` unchanged.
- Do not style `.kit-button__label`, `.kit-button__short-label`, or Button icons from PullDetail.
- Preserve independent command-button semantics; use `FitStages`, not the radio-group `SegmentedControl`.
- Use the measured Actions trigger as the final fit stage; do not use a fixed-width menu fallback.

---

### Task 1: Guard adaptive action rendering

**Files:**
- Modify: `frontend/tests/e2e-full/detail-action-buttons.spec.ts`

**Interfaces:**
- Consumes: the pull-detail action row, Kit UI Button, and Kit UI FitStages.
- Produces: a browser regression that detects loss of the compact fit stage or Button-internal consumer overrides.

- [ ] **Step 1: Write the failing test**

Load draft pull request `acme/widgets#6`, constrain `.pull-detail-content` to
the medium stage, and assert that `FitStages` selects compact labels; Ready,
Approve, Merge, and Close retain their icons; and each visible normal label
wrapper's midpoint matches its button midpoint.

- [ ] **Step 2: Run the test to verify it fails**

Run:

```bash
cd frontend
../node_modules/.bin/playwright test --config=playwright-e2e.config.ts --project=chromium tests/e2e-full/detail-action-buttons.spec.ts --grep "uses the compact Kit UI fit stage"
```

Expected: FAIL because PullDetail does not render a Kit UI `FitStages` host.

### Task 2: Render measured full and compact stages

**Files:**
- Modify: `packages/ui/src/components/detail/PullDetail.svelte`
- Modify: `packages/ui/src/components/detail/ReadyForReviewButton.svelte`
- Modify: `packages/ui/src/components/detail/ApproveWorkflowsButton.svelte`

**Interfaces:**
- Consumes: Kit UI Button's existing internal rendering and Kit UI FitStages.
- Produces: measured PullDetail action stages without altering Button internals.

- [ ] **Step 1: Add explicit label variants**

Add `compactLabel?: boolean` to the local action components whose labels
actually differ. Select compact or full text before passing it through
Button's normal `label` prop.

- [ ] **Step 2: Render FitStages**

Give `FitStages` stateless full-label, compact-label, and Actions-trigger probes,
and bind its selected stage. Render the real stateful controls once outside the
measurement host, changing only their labels/layout from that stage. Remove the
legacy Button-internal overrides and the fixed 340px whole-row/menu transition.

- [ ] **Step 3: Run focused tests**

Run the browser regression from Task 1 and:

```bash
cd frontend
../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/detail/PullDetail.test.ts
cd ..
./node_modules/.bin/vp run ui-package-typecheck
```

Expected: all commands pass.

- [ ] **Step 4: Validate Svelte**

Run the Svelte autofixer on all modified components:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/PullDetail.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/ReadyForReviewButton.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/ApproveWorkflowsButton.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/ApproveButton.svelte
```

Expected: no required fixes.

### Task 3: Verify the live pane

**Files:**
- No code changes.

**Interfaces:**
- Consumes: the running ephemeral Middleman frontend.
- Produces: visual confirmation that the actual resizable pane selects centered full and compact action stages.

- [ ] **Step 1: Inspect with Computer Use**

Resize the live pull-detail pane through the wide, medium, and narrow stages.
Confirm labels remain vertically centered and the Close icon remains present
in both visible FitStages rows.

- [ ] **Step 2: Run the required frontend suite**

Run the full Vitest suite and affected full-stack Playwright file after the
final frontend edit, then run `make frontend-check`.
