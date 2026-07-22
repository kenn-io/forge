# Kata Repository Typeahead Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the manual Kata project repository picker searchable by replacing its non-filterable dropdown with kit-ui's existing `Typeahead`.

**Architecture:** Keep mapping identity and persistence unchanged. Adapt the existing `RepoOption` list to kit-ui's `TypeaheadOption` shape in `KataProjectMappingsSettings`, then bind `onselect` to the existing draft repository key.

**Tech Stack:** Svelte 5, TypeScript, kit-ui `Typeahead`, Testing Library, Vite+ Vitest

## Global Constraints

- Reuse kit-ui's existing `Typeahead`; do not change kit-ui or add another picker implementation.
- Search locally across the current display-name and repository-path label.
- Preserve the provider-qualified repository key used by `repoOptionsByKey` and mapping persistence.
- Leave daemon and other short enumerated controls on `SelectDropdown`.
- Use Vite+ commands, never npm.

---

### Task 1: Replace the repository dropdown with the existing typeahead

**Files:**
- Modify: `frontend/src/lib/components/settings/KataProjectMappingsSettings.svelte:1-92,434-441,572-585`
- Test: `frontend/src/lib/components/settings/KataProjectMappingsSettings.test.ts:80-208`

**Interfaces:**
- Consumes: kit-ui `Typeahead` and `TypeaheadOption`; existing `RepoOption.key`, `RepoOption.label`, `draft.repoKey`, and `repoOptionsByKey`.
- Produces: `repoTypeaheadOptions: TypeaheadOption[]`, where `name` is the existing provider-qualified repository key and `label` is the existing display-name plus repository-path label.

- [ ] **Step 1: Write the failing interaction test**

Extend the `saves a Kata mapping to a selected known Middleman project` fixture with a second repository target:

```ts
{
  display_name: "Tools",
  repo: {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "tools",
    repo_path: "acme/tools",
    capabilities: defaultProviderCapabilities,
  },
},
```

Replace the dropdown interaction with the typeahead contract:

```ts
const pickerName = "Kata project project-kata repository target";
await fireEvent.click(screen.getByRole("button", { name: pickerName }));
const query = screen.getByRole("combobox", { name: pickerName });
await fireEvent.input(query, { target: { value: "middle" } });

expect(screen.getByRole("option", { name: "Middleman · kenn-io/middleman" })).toBeTruthy();
expect(screen.queryByRole("option", { name: "Tools · acme/tools" })).toBeNull();
await fireEvent.mouseDown(screen.getByRole("option", { name: "Middleman · kenn-io/middleman" }));
```

Update the two closed-control assertions to use the typeahead trigger's button role:

```ts
expect(screen.getByRole("button", { name: /project-kata repository target/ }).textContent).toContain("Middleman");
expect(screen.getByRole("button", { name: /project-unmapped repository target/ }).textContent).toContain(
  "Select a repository",
);
```

- [ ] **Step 2: Run the focused test and verify it fails for the missing typeahead**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/components/settings/KataProjectMappingsSettings.test.ts
```

Expected: FAIL because the closed repository control is still a `combobox` rather than the expected typeahead trigger button.

- [ ] **Step 3: Implement the minimal Typeahead adapter and control replacement**

Import the existing kit primitive:

```ts
import { Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
```

Replace `repoSelectOptions` with a typeahead-shaped derived value:

```ts
const repoTypeaheadOptions = $derived<TypeaheadOption[]>(
  repoOptions.map((option) => ({ name: option.key, label: option.label })),
);
```

Replace only the repository-target control:

```svelte
<Typeahead
  value={draft.repoKey}
  options={repoTypeaheadOptions}
  fallbackLabel="Select a repository"
  placeholder={`Kata project ${label} repository target`}
  emptyLabel="No matching repositories"
  onselect={(value) => {
    draft.repoKey = value;
  }}
  disabled={embedded || saving}
/>
```

Replace the table's old SelectDropdown layout overrides with public Typeahead sizing knobs:

```css
.mapping-table :global(.kit-typeahead) {
  width: 100%;
  min-width: 0;
  max-width: none;
  --typeahead-control-height: 30px;
  --typeahead-control-font-size: var(--font-size-sm);
}
```

- [ ] **Step 4: Run the focused test and verify it passes**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/components/settings/KataProjectMappingsSettings.test.ts
```

Expected: PASS with the filter assertion and existing mapping-save assertion both satisfied.

- [ ] **Step 5: Validate the Svelte component**

Run:

```bash
vp exec -- svelte-mcp svelte-autofixer ./frontend/src/lib/components/settings/KataProjectMappingsSettings.svelte --svelte-version 5
```

Expected: no Svelte errors or required fixes.

Run:

```bash
make frontend-check
```

Expected: formatting, lint, type, Svelte, and kit-ui checks pass.

- [ ] **Step 6: Run the full affected frontend suite**

Run:

```bash
cd frontend && ../node_modules/.bin/vp test run --project unit
```

Expected: all frontend unit tests pass.

- [ ] **Step 7: Sync context and commit the implementation**

Run `scripts/context-sync --check`, inspect the intended diff under the repository-local `context-sync --commit` workflow, then stage only:

```text
docs/superpowers/plans/2026-07-22-kata-repository-typeahead.md
frontend/src/lib/components/settings/KataProjectMappingsSettings.svelte
frontend/src/lib/components/settings/KataProjectMappingsSettings.test.ts
```

Create a normal hook-verified commit using the mandatory commit skill with subject:

```text
fix: make Kata repository mappings searchable
```
