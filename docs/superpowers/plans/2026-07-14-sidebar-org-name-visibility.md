# Sidebar Organization-Name Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose a consistently named **Hide org name** control in PR, issue, and workspace sidebar menus and make each sidebar render labels from its persisted preference.

**Architecture:** PR and issue menus use the existing shared grouping preference and the shared collision-safe repository label formatter. The workspace menu keeps its independent display preference but presents the same positive “Hide org name” wording and active-state polarity.

**Tech Stack:** Svelte 5, TypeScript, `@kenn-io/kit-ui` `FilterDropdown`, Vitest browser/jsdom, Playwright full-stack tests.

## Global Constraints

- Keep PR/issue/activity on `middleman:hideOrgName`.
- Keep workspace display persistence independent.
- Preserve provider/host disambiguation when organization names are hidden.
- Keep repository chip color tied to full repository identity when display labels change.
- Use existing shared filter and repository-label primitives.

---

### Task 1: Pin the sidebar menu behavior with failing tests

**Files:**

- Modify: `frontend/src/App.grouping-toggle.browser.svelte.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts`

**Interfaces:**

- Consumes: existing app browser harness and `FilterDropdown` accessible button labels.
- Produces: regression coverage for the shared PR/issue preference and workspace inverse-label behavior.

- [x] Add browser assertions that PR and issue compact filter menus contain **Hide org name**, that selecting it hides the owner portion of visible repository labels, and that the state persists when moving between PR and issue routes.
- [x] Update workspace tests to query **Hide org name**, assert it starts inactive when `showOrgNames` is true, and assert selection hides organization names.
- [x] Run the focused browser and workspace tests from `frontend/` and confirm the new assertions fail because the controls or wording are absent.

### Task 2: Implement shared PR and issue visibility controls

**Files:**

- Modify: `packages/ui/src/components/sidebar/PullList.svelte`
- Modify: `packages/ui/src/components/sidebar/IssueList.svelte`
- Modify: `packages/ui/src/components/sidebar/PullItem.svelte`
- Modify: `packages/ui/src/components/sidebar/IssueItem.svelte`
- Modify: `packages/ui/src/views/FocusListView.svelte`

**Interfaces:**

- Consumes: `grouping.getHideOrgName()`, `grouping.setHideOrgName(boolean)`, and `createRepoLabelFormatter(repos, { showOrgNames })`.
- Produces: `repoLabel: string` props for PR/issue rows and collision-safe group labels.

- [x] Add a Visibility section containing an item with `id: "hide-org-name"`, `label: "Hide org name"`, `active: grouping.getHideOrgName()`, and a selector that toggles the preference.
- [x] Include this preference in compact-menu changed-state, count, and reset behavior.
- [x] Build a repository-label formatter from the visible PRs/issues and use it for grouped headers and flat/workflow row repo chips.
- [x] Replace each item component's locally concatenated owner/repo label with its parent-provided collision-safe `repoLabel` prop.
- [x] Run the focused browser test and confirm PR/issue assertions pass.
- [x] Keep issue visibility reachable above and below its compact breakpoint, with full-stack responsive coverage.
- [x] Cover compact badge/reset behavior and workspace active-state polarity.
- [x] Apply the shared formatter to responsive focus/mobile lists and cover both item types in full-stack mobile tests.

### Task 3: Normalize the workspace menu wording

**Files:**

- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts`

**Interfaces:**

- Consumes: existing `displayOptions.showOrgNames` boolean.
- Produces: a checked **Hide org name** item when `showOrgNames` is false.

- [x] Rename the item id to `hide-org-name`, label it **Hide org name**, describe the hidden state, and invert its `active` value while retaining the existing toggle callback.
- [x] Run the focused workspace test and confirm its menu and rendering assertions pass.

### Task 4: Validate and commit

**Files:**

- Modify: `docs/superpowers/specs/2026-07-14-sidebar-org-name-visibility-design.md` only if implementation disproves a factual assumption.
- Modify: this plan to check completed steps.

**Interfaces:**

- Consumes: repository frontend validation commands.
- Produces: a hook-verified implementation commit.

- [x] Run `./node_modules/.bin/vp exec svelte-mcp svelte-autofixer` for every changed `.svelte` file and address actionable findings.
- [x] Run the focused tests, then the full frontend `vp test` suite because frontend behavior changed.
- [x] Run `make frontend-check-no-deps`.
- [x] Review `git diff`, run the context-sync stop decision, and commit all scoped files with hooks enabled.
