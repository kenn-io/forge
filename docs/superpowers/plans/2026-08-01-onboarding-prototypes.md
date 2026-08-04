# Onboarding Prototype Gallery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an interactive gallery of three research-backed onboarding prototypes to the hidden design-system route.

**Approved spec/design:** `docs/superpowers/specs/2026-08-01-onboarding-prototypes-design.md`

**Architecture:** A small gallery component owns prototype selection and delegates each direction to a focused, state-local Svelte component. `DesignSystemPage.svelte` only places the gallery; the prototypes make no API calls and share no state with the real app.

**Tech Stack:** Svelte 5 runes, TypeScript, Vitest with Testing Library, existing Kenn Forge theme tokens, Lucide Svelte icons.

## Global Constraints

- Keep all repository and pull-request data generic and synthetic.
- Do not call application APIs or persist any prototype state.
- Do not alter normal application routes or first-run behavior.
- Use the shared application color and spacing tokens.
- Keep every interaction keyboard reachable and expose selected/progress state semantically.

---

### Task 1: Gallery switching contract

**Files:**

- Create: `frontend/src/lib/components/design-system/OnboardingPrototypeGallery.test.ts`

**Interfaces:**

- Consumes: The gallery component produced in Task 2.
- Produces: An interaction contract for default selection and tab switching.

- [ ] **Step 1: Write the failing interaction test**

Render `OnboardingPrototypeGallery`, assert the wizard is selected by default,
click the checklist and guide tabs, and assert each corresponding heading is
visible while the selected tab state moves. Add focused cases for the wizard's
repository filter and progress handoff, the checklist's repository activation,
and the guide's task switching.

```ts
import { fireEvent, render, screen } from "@testing-library/svelte";
import { describe, expect, it } from "vitest";
import OnboardingPrototypeGallery from "./OnboardingPrototypeGallery.svelte";

describe("OnboardingPrototypeGallery", () => {
  it("switches between the three onboarding directions", async () => {
    render(OnboardingPrototypeGallery);

    const wizard = screen.getByRole("tab", { name: "Focused setup" });
    expect(wizard.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("heading", { name: "Choose the repositories you maintain" })).toBeTruthy();

    const checklist = screen.getByRole("tab", { name: "Activation checklist" });
    await fireEvent.click(checklist);
    expect(checklist.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("heading", { name: "Your first useful session" })).toBeTruthy();

    const guide = screen.getByRole("tab", { name: "Start guide" });
    await fireEvent.click(guide);
    expect(guide.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("heading", { name: "From gh to a working PR" })).toBeTruthy();
  });

  it("filters wizard repositories and advances to first sync", async () => {
    render(OnboardingPrototypeGallery);
    await fireEvent.input(screen.getByRole("searchbox", { name: "Filter repositories" }), {
      target: { value: "docs" },
    });
    expect(screen.getByText("acme/docs")).toBeTruthy();
    expect(screen.queryByText("acme/forge")).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: /Configure 1 repository/ }));
    expect(screen.getByRole("heading", { name: "First sync is underway" })).toBeTruthy();
  });

  it("activates repository setup from the in-shell checklist", async () => {
    render(OnboardingPrototypeGallery);
    await fireEvent.click(screen.getByRole("tab", { name: "Activation checklist" }));
    await fireEvent.click(screen.getByRole("button", { name: "Choose repositories" }));
    expect(screen.getByRole("heading", { name: "Select repositories" })).toBeTruthy();
  });

  it("moves the start guide between activation tasks", async () => {
    render(OnboardingPrototypeGallery);
    await fireEvent.click(screen.getByRole("tab", { name: "Start guide" }));
    await fireEvent.click(screen.getByRole("button", { name: "2. Add repositories" }));
    expect(screen.getByRole("heading", { name: "Choose what Kenn Forge should track" })).toBeTruthy();
  });
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/components/design-system/OnboardingPrototypeGallery.test.ts`

Expected: FAIL because the gallery component does not exist.

### Task 2: Gallery and three interactive prototypes

**Files:**

- Create: `frontend/src/lib/components/design-system/OnboardingPrototypeGallery.svelte`
- Create: `frontend/src/lib/components/design-system/OnboardingWizardPrototype.svelte`
- Create: `frontend/src/lib/components/design-system/OnboardingChecklistPrototype.svelte`
- Create: `frontend/src/lib/components/design-system/OnboardingGuidePrototype.svelte`

**Interfaces:**

- Consumes: No props and no application context; all display data is module-local synthetic data.
- Produces: Default export `OnboardingPrototypeGallery`, rendered without props, and three internal prototype components.

- [ ] **Step 1: Implement the gallery shell**

Use a `PrototypeId` union and reactive `active` value. Render a labelled
tablist whose buttons use `role="tab"`, `aria-selected`, and keyed metadata.
Render exactly one prototype beneath a comparison note with explicit `idea`,
`strength`, and `tradeoff` fields.

```svelte
<script lang="ts">
  type PrototypeId = "wizard" | "checklist" | "guide";
  let active = $state<PrototypeId>("wizard");
</script>

<div role="tablist" aria-label="Onboarding prototype">
  {#each prototypes as prototype (prototype.id)}
    <button role="tab" aria-selected={active === prototype.id} onclick={() => { active = prototype.id; }}>
      {prototype.label}
    </button>
  {/each}
</div>
```

- [ ] **Step 2: Implement the focused wizard**

Add a compact five-step progress rail, authenticated `gh` status, searchable
repository multi-select, first-sync handoff, and a mock PR-to-workspace action.

- [ ] **Step 3: Implement the in-shell checklist**

Add a Kenn Forge-like top bar and a resumable activation rail that explicitly
shows authenticated `gh`, repository selection, first-sync progress, the first
opened PR, and a contextual workspace milestone. Inline repository selection
populates the mock PR list without leaving the shell.

- [ ] **Step 4: Implement the task-oriented guide**

Add a concise start-page navigation and three task sections that collectively
show authenticated `gh`, repository selection, visible first-sync progress,
the first opened PR, and workspace launch. The adjacent live product preview
changes with the selected task.

- [ ] **Step 5: Run the focused test and verify GREEN**

Run the Task 1 command.

Expected: PASS.

- [ ] **Step 6: Validate all Svelte files**

Run these from the repository root and fix actionable findings:

```sh
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/design-system/OnboardingPrototypeGallery.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/design-system/OnboardingWizardPrototype.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/design-system/OnboardingChecklistPrototype.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/design-system/OnboardingGuidePrototype.svelte
```

### Task 3: Place and verify the gallery

**Files:**

- Modify: `frontend/src/lib/components/design-system/DesignSystemPage.svelte`
- Create: `frontend/src/lib/components/design-system/DesignSystemPage.test.ts`

**Interfaces:**

- Consumes: `OnboardingPrototypeGallery`.
- Produces: An onboarding-prototypes section near the top of `/design-system`.

- [ ] **Step 1: Write and run the failing placement test**

Render `DesignSystemPage` and assert the `Onboarding prototypes` heading and
the labelled onboarding prototype tablist are present. Run the focused test
and verify it fails because the page does not render the gallery yet.

- [ ] **Step 2: Add the gallery to the design-system page**

Import the gallery and render it after the page introduction with a concise
research/prototype heading.

- [ ] **Step 3: Validate the modified design-system page**

Run:

```sh
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/design-system/DesignSystemPage.svelte
```

- [ ] **Step 4: Run focused tests**

Run the gallery and `DesignSystemPage` tests from the frontend Vitest unit
project.

- [ ] **Step 5: Run frontend checks and build**

Run `./node_modules/.bin/vp run frontend-check` and
`cd frontend && ../node_modules/.bin/vp build --logLevel warn`.

- [ ] **Step 6: Inspect all three prototypes in the browser**

Start the isolated stack with `make dev-ephemeral`, open `/design-system`,
switch through every prototype, exercise repository selection and task
progress, and inspect the desktop and narrow gallery layouts. Confirm there is
no decorative motion, including with reduced-motion emulation. Use the
generated status JSON for the selected frontend URL.

- [ ] **Step 7: Commit the completed prototype gallery**

Run repository context sync in commit mode, then create a conventional commit
that explains why the three directions are kept as a contained comparison
surface.
