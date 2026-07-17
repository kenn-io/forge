# Detail Card Action Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore quiet detail-card edit, copy, and delete controls without hiding commit identity metadata or making actions undiscoverable to keyboard and touch users.

**Architecture:** Keep the behavior local to `EventTimeline.svelte` by styling kit `CommentCard` action wrappers only inside timeline cards. Exclude commit cards, reveal actions for card hover and focus-within, and override the hidden state for non-hover or coarse-pointer devices.

**Tech Stack:** Svelte 5 component CSS, kit-ui `CommentCard`, Vite+ unit tests, Svelte compiler CSS inspection.

## Global Constraints

- Do not change the general kit-ui `Card` or `CommentCard` contract.
- Do not change compact-row or inline-reply action behavior.
- Commit SHAs remain visible at rest.
- Pointer-hover, keyboard-focus, and touch behavior must all remain supported.

---

### Task 1: Restore detail-card action reveal

**Files:**
- Modify: `packages/ui/src/components/detail/EventTimeline.svelte`
- Test: `packages/ui/src/components/detail/EventTimeline.test.ts`

**Interfaces:**
- Consumes: kit-ui `.kit-comment-card` and `.kit-card__actions` BEM classes plus middleman's `.event-card--commit` marker.
- Produces: a timeline-local CSS interaction contract; no TypeScript or component API changes.

- [x] **Step 1: Write the failing CSS-contract test**

Add a focused test beside the existing comment-action coverage. Inspect the compiled component CSS and assert that ordinary card actions start with `opacity: 0` and `pointer-events: none`, hover/focus rules restore both properties, the hidden selector excludes `.event-card--commit`, and the touch media rule restores visibility.

```ts
it("reveals detail-card actions on hover, focus, and touch without hiding commit SHAs", () => {
  const hiddenActions = findCompiledStyleRule(
    ".kit-comment-card:not(.event-card--commit) .kit-card__actions",
    [":hover", ":focus-within"],
  );

  expect(hiddenActions.getPropertyValue("opacity")).toBe("0");
  expect(hiddenActions.getPropertyValue("pointer-events")).toBe("none");
  expect(findCompiledStyleRule(":hover .kit-card__actions").getPropertyValue("opacity")).toBe("1");
  expect(findCompiledStyleRule(":focus-within .kit-card__actions").getPropertyValue("opacity")).toBe("1");

  const style = document.createElement("style");
  style.textContent = compiledCss;
  document.head.appendChild(style);
  const touchMedia = Array.from(style.sheet?.cssRules ?? []).find(
    (rule): rule is CSSMediaRule =>
      "conditionText" in rule && rule.conditionText === "(hover: none), (pointer: coarse)",
  );
  const touchActions = Array.from(touchMedia?.cssRules ?? []).find(
    (rule): rule is CSSStyleRule =>
      "selectorText" in rule && rule.selectorText.includes(":not(.event-card--commit)"),
  );

  expect(touchActions?.style.opacity).toBe("1");
  expect(touchActions?.style.pointerEvents).toBe("auto");
});
```

- [x] **Step 2: Run the test to verify it fails for the regression**

Run from `frontend/`:

```bash
../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/detail/EventTimeline.test.ts
```

Expected: FAIL because no hidden-action selector exists after the kit-ui migration.

- [x] **Step 3: Add the minimal timeline-local CSS**

Add the following rules near the existing commit-card header overrides:

```css
:global(.kit-comment-card:not(.event-card--commit) .kit-card__actions) {
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.15s;
}

:global(.kit-comment-card:not(.event-card--commit):hover .kit-card__actions),
:global(.kit-comment-card:not(.event-card--commit):focus-within .kit-card__actions) {
  opacity: 1;
  pointer-events: auto;
}

@media (hover: none), (pointer: coarse) {
  :global(.kit-comment-card:not(.event-card--commit) .kit-card__actions) {
    opacity: 1;
    pointer-events: auto;
  }
}
```

- [x] **Step 4: Run focused and package verification**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit ../packages/ui/src/components/detail/EventTimeline.test.ts
cd .. && ./node_modules/.bin/vp run ui-package-check
./node_modules/.bin/vp run kit-ui-check
```

Expected: EventTimeline tests pass; package checks exit 0; kit-ui-check reports zero findings.

- [x] **Step 5: Analyze the changed Svelte component and commit**

Run:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/EventTimeline.svelte --svelte-version 5
```

Review the diff, stage only the plan, component, and focused test, then create a hook-enforced conventional commit describing the restored interaction.
