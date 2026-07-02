# Provider Brand Icons Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show provider brand icons for GitHub, GitLab, Forgejo, and Gitea when more than one provider is present.

**Architecture:** Add one focused Svelte component that renders Simple Icons SVG path data by provider id. Each surface computes whether its local data includes multiple providers and only passes icons through in that state.

**Tech Stack:** Svelte 5, TypeScript, Bun, Vitest, Testing Library, `simple-icons`.

---

### Task 1: Provider Icon Component

**Files:**
- Create: `frontend/src/lib/components/provider/ProviderIcon.svelte`
- Create: `frontend/src/lib/components/provider/provider-icons.ts`
- Test: `frontend/src/lib/components/provider/ProviderIcon.test.ts`

- [ ] Write a failing component test for GitHub/GitLab rendering and unknown-provider fallback.
- [ ] Add `simple-icons` to `frontend/package.json` with Bun.
- [ ] Implement a typed provider-to-icon map using `siGithub`, `siGitlab`, `siForgejo`, and `siGitea`.
- [ ] Render a 24x24 SVG with `role="img"` and an accessible label.

### Task 2: Repository Cards

**Files:**
- Modify: `frontend/src/lib/components/repositories/RepoSummaryPage.svelte`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryCard.svelte`
- Test: `frontend/src/lib/components/repositories/RepoSummaryPage.test.ts`

- [ ] Add a failing test that multi-provider repo summaries show provider icons on cards.
- [ ] Add a failing test that single-provider summaries do not show provider icons.
- [ ] Compute `showProviderIcons` from distinct `summary.repo.provider` values on the page.
- [ ] Pass `showProviderIcon` to `RepoSummaryCard` and render the icon before the repo name.

### Task 3: Workspace Sidebar

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`
- Test: `frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts`

- [ ] Add provider to the workspace row type if the API response already provides it.
- [ ] Add a failing test that multiple workspace providers show icons in repo group headers.
- [ ] Add a failing test that a single provider hides icons.
- [ ] Compute `showProviderIcons` from distinct workspace providers and render the icon in group headers.

### Task 4: Settings and Project Identity

**Files:**
- Modify: `frontend/src/lib/components/settings/RepoSettings.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceProjectCard.svelte`
- Test: `frontend/src/lib/components/terminal/WorkspaceProjectCard.test.ts`

- [ ] Add a failing settings test that multiple configured providers show row icons.
- [ ] Add a failing project-card test that a multi-provider context shows the linked project icon.
- [ ] Render provider icons beside configured repo rows when multiple providers are configured.
- [ ] Render provider icons beside project identity when a provider id is available.

### Task 5: Verification and Commit

- [ ] Run focused Vitest tests for touched components.
- [ ] Run `bun run typecheck`.
- [ ] Run Svelte autofixer on touched `.svelte` files.
- [ ] Commit with hooks using a conventional commit message explaining the provider-disambiguation outcome.
