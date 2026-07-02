# Route-Stable Responsive Details Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep canonical PR and issue URLs stable while narrow viewports render the same focus presentation used by `/focus`.

**Architecture:** Remove viewport-driven navigation from `App.svelte` and replace it with derived presentation selection. Reuse `PRListView`, `IssueListView`, and `FocusListView` for both explicit `/focus` routes and narrow canonical routes, with route-family-aware builders so canonical routes stay canonical.

**Tech Stack:** Svelte 5, TypeScript, Bun, Playwright e2e, shared `@middleman/ui` route helpers.

---

### Task 1: Write Failing E2E Coverage

**Files:**
- Modify: `frontend/tests/e2e-full/mobile-routes.spec.ts`
- Modify: `frontend/tests/e2e-full/routed-items.spec.ts`

- [ ] **Step 1: Replace redirect assertions for canonical phone detail routes**

In `frontend/tests/e2e-full/mobile-routes.spec.ts`, change the existing canonical desktop deep-link tests to assert stable URLs:

```ts
test("phone canonical PR files deep link renders focus presentation without changing URL", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/1/files");

  await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1\/files$/);
  await expect(page.locator(".focus-layout .files-layout")).toBeVisible();
  await expect(page.locator(".focus-layout .diff-view")).toBeVisible();
  await expect(page.locator(".mobile-shell")).toHaveCount(0);
});

test("phone canonical issue deep link renders focus presentation without changing URL", async ({ page }) => {
  await page.goto("/issues/github/acme/widgets/10");

  await expect(page).toHaveURL(/\/issues\/github\/acme\/widgets\/10$/);
  await expect(page.locator(".focus-layout .issue-detail .detail-title")).toBeVisible();
  await expect(page.locator(".mobile-shell")).toHaveCount(0);
});
```

- [ ] **Step 2: Add canonical narrow list route-family coverage**

In `frontend/tests/e2e-full/routed-items.spec.ts`, add tests that phone-sized canonical lists use focus presentation but click to canonical URLs:

```ts
test("narrow canonical PR list routes selected rows to canonical detail", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/pulls");
  await page.locator(".focus-list .pull-item").first()
    .waitFor({ state: "visible", timeout: 10_000 });

  await page.locator(".focus-list .pull-item").filter({ hasText: prTitle }).first().click();

  await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1$/);
  await expect(page.locator(".focus-layout .pull-detail .detail-title"))
    .toContainText(prTitle);
});

test("narrow canonical issue list routes selected rows to canonical detail", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/issues");
  await page.locator(".focus-list .issue-item").first()
    .waitFor({ state: "visible", timeout: 10_000 });

  await page.locator(".focus-list .issue-item").filter({ hasText: issueTitle }).first().click();

  await expect(page).toHaveURL(/\/issues\/github\/acme\/widgets\/10$/);
  await expect(page.locator(".focus-layout .issue-detail .detail-title"))
    .toContainText(issueTitle);
});
```

- [ ] **Step 3: Run tests to verify RED**

Run:

```bash
bun run --cwd frontend test:e2e:mock tests/e2e-full/mobile-routes.spec.ts -g "canonical .* without changing URL"
bun run --cwd frontend test:e2e:mock tests/e2e-full/routed-items.spec.ts -g "narrow canonical"
```

Expected: tests fail because canonical routes still redirect or list clicks still use focus route builders.

### Task 2: Make FocusListView Route-Family Aware

**Files:**
- Modify: `packages/ui/src/views/FocusListView.svelte`

- [ ] **Step 1: Add route-family prop and canonical builders**

Add imports and prop:

```ts
import {
  buildFocusIssueRoute,
  buildFocusPullRequestRoute,
  buildIssueRoute,
  buildPullRequestRoute,
  type IssueRouteRef,
  type PullRequestRouteRef,
} from "../routes.js";

type RouteFamily = "focus" | "canonical";

interface Props {
  listType: "mrs" | "issues";
  repo?: string;
  routeFamily?: RouteFamily;
}

const { listType, repo, routeFamily = "focus" }: Props = $props();
```

Update selection:

```ts
function handlePRSelect(ref: PullRequestRouteRef): void {
  navigate(routeFamily === "canonical"
    ? buildPullRequestRoute(ref)
    : buildFocusPullRequestRoute(ref));
}

function handleIssueSelect(ref: IssueRouteRef): void {
  navigate(routeFamily === "canonical"
    ? buildIssueRoute(ref)
    : buildFocusIssueRoute(ref));
}
```

- [ ] **Step 2: Run Svelte autofixer**

Run:

```bash
npx @sveltejs/mcp@0.1.22 svelte-autofixer packages/ui/src/views/FocusListView.svelte --svelte-version 5
```

Expected: no blocking Svelte issues.

### Task 3: Select Focus Presentation Without URL Rewrites

**Files:**
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Remove automatic responsive redirect**

Remove the `redirectPhoneToMobileRoute()` mount/effect path and any helper used only by that path. Keep explicit mobile tab navigation and the explicit desktop opt-out button.

- [ ] **Step 2: Add focus presentation helpers**

Add helpers in `App.svelte`:

```ts
function shouldUseResponsiveFocusPresentation(): boolean {
  const route = getRoute();
  if (!isPhoneViewport() && !shouldForceMobileRoutes()) return false;
  if (route.page === "pulls") return route.view === "list";
  return route.page === "issues";
}

function useFocusLayoutClass(): boolean {
  return isPhoneViewport() || shouldForceMobileRoutes();
}
```

- [ ] **Step 3: Reuse focus layout for canonical pulls/issues**

Change the top route branch so it renders `.focus-layout` when either `route.page === "focus"` or `shouldUseResponsiveFocusPresentation()` is true. For canonical `/pulls`:

```svelte
{:else if r.page === "pulls"}
  {#if r.selected}
    <PRListView
      selectedPR={r.selected}
      detailTab={r.tab === "files" ? "files" : "conversation"}
      isSidebarCollapsed={true}
      hideSidebar={true}
      showStackSidebar={false}
    />
  {:else}
    <FocusListView listType="mrs" routeFamily="canonical" />
  {/if}
```

For canonical `/issues`:

```svelte
{:else if r.page === "issues"}
  {#if r.selected}
    <IssueListView
      selectedIssue={r.selected}
      isSidebarCollapsed={true}
      hideSidebar={true}
    />
  {:else}
    <FocusListView listType="issues" routeFamily="canonical" />
  {/if}
```

- [ ] **Step 4: Run Svelte autofixer**

Run:

```bash
npx @sveltejs/mcp@0.1.22 svelte-autofixer frontend/src/App.svelte --svelte-version 5
```

Expected: no blocking Svelte issues.

### Task 4: Verify and Commit

**Files:**
- Modify: test and Svelte files from prior tasks.

- [ ] **Step 1: Run focused e2e tests**

Run:

```bash
bun run --cwd frontend test:e2e:mock tests/e2e-full/mobile-routes.spec.ts -g "phone canonical|focused PR files"
bun run --cwd frontend test:e2e:mock tests/e2e-full/routed-items.spec.ts -g "narrow canonical|focus .* routes selected"
```

Expected: all selected tests pass.

- [ ] **Step 2: Run type and Svelte checks**

Run:

```bash
bun run --cwd frontend check
bunx --cwd frontend tsc --noEmit -p ./tsconfig.json
```

Expected: both commands pass.

- [ ] **Step 3: Commit**

Run:

```bash
git add frontend/src/App.svelte frontend/tests/e2e-full/mobile-routes.spec.ts frontend/tests/e2e-full/routed-items.spec.ts packages/ui/src/views/FocusListView.svelte docs/superpowers/plans/2026-05-16-route-stable-responsive-details.md
git commit -m "fix: keep responsive detail routes stable"
```

Expected: commit hooks pass and the commit records the route-stable responsive behavior.
