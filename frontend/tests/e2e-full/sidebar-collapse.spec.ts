import { expect, test, type Locator, type Page } from "@playwright/test";
import { startIsolatedE2EServer } from "./support/e2eServer";

type PullListRow = {
  Title: string;
  ReviewDecision: string;
  KanbanStatus: string;
};

type PullDetailRow = {
  merge_request: PullListRow;
};

async function waitForPRList(page: Page): Promise<void> {
  await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function waitForIssueList(page: Page): Promise<void> {
  await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function sidebarWidth(sidebar: Locator): Promise<number> {
  return Math.round(await sidebar.evaluate((node) => node.getBoundingClientRect().width));
}

function collapseToggle(sidebar: Locator): Locator {
  return sidebar.getByRole("button", { name: "Collapse sidebar" });
}

function expandToggle(sidebar: Locator): Locator {
  return sidebar.getByRole("button", { name: "Expand sidebar" });
}

function sidebarResizeHandle(page: Page): Locator {
  return page.getByRole("button", { name: "Resize sidebar" });
}

async function dragResizeHandle(page: Page, handle: Locator, deltaX: number): Promise<void> {
  const box = await handle.boundingBox();
  expect(box).not.toBeNull();
  if (!box) {
    throw new Error("resize handle missing");
  }

  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2 + deltaX, box.y + box.height / 2, {
    steps: 10,
  });
  await page.mouse.up();
}

async function expectResizedSidebar(
  page: Page,
  path: string,
  waitForList: (page: Page) => Promise<void>,
): Promise<void> {
  await page.goto(path);
  await waitForList(page);

  const sidebar = page.locator(".sidebar").first();
  const handle = sidebarResizeHandle(page);

  expect(await sidebarWidth(sidebar)).toBe(340);

  await dragResizeHandle(page, handle, 80);

  await expect.poll(async () => sidebarWidth(sidebar)).toBe(420);

  await page.reload();
  await waitForList(page);

  await expect.poll(async () => sidebarWidth(page.locator(".sidebar").first())).toBe(420);
}

async function expectCompactFiltersAtMinimumWidth(
  page: Page,
  path: string,
  waitForList: (page: Page) => Promise<void>,
): Promise<void> {
  await page.goto(path);
  await waitForList(page);

  const sidebar = page.locator(".sidebar").first();
  const handle = sidebarResizeHandle(page);

  await dragResizeHandle(page, handle, -220);
  await expect.poll(async () => sidebarWidth(sidebar)).toBe(200);

  const filterBar = sidebar.locator(".filter-bar").first();
  const compactFilters = filterBar.locator(".compact-filter-menu .filter-btn");
  await expect(compactFilters).toBeVisible();
  await expect(filterBar.locator(".state-toggle")).toBeHidden();
  await expect(filterBar.locator(".group-toggle")).toBeHidden();

  const filterMetrics = await filterBar.evaluate((node) => ({
    clientWidth: node.clientWidth,
    scrollWidth: node.scrollWidth,
  }));
  expect(filterMetrics.scrollWidth).toBeLessThanOrEqual(filterMetrics.clientWidth);
}

async function expectCompactFiltersInNarrowViewport(
  page: Page,
  path: string,
  waitForList: (page: Page) => Promise<void>,
): Promise<void> {
  await page.setViewportSize({ width: 545, height: 954 });
  const desktopPath = path.includes("?") ? `${path}&desktop=1` : `${path}?desktop=1`;
  await page.goto(desktopPath);
  await waitForList(page);

  const sidebar = page.locator(".sidebar").first();
  const filterBar = sidebar.locator(".filter-bar").first();
  await expect(filterBar.locator(".compact-filter-menu .filter-btn")).toBeVisible();
  await expect(filterBar.locator(".state-toggle")).toBeHidden();
  await expect(filterBar.locator(".group-toggle")).toBeHidden();
}

async function setPersistedSidebarWidth(
  page: Page,
  path: string,
  width: number,
  waitForList: (page: Page) => Promise<void>,
): Promise<Locator> {
  await page.goto(path);
  await page.evaluate((value) => {
    localStorage.setItem("middleman-sidebar-width", String(value));
    localStorage.removeItem("middleman-sidebar");
  }, width);
  await page.reload();
  await waitForList(page);
  return page.locator(".sidebar").first().locator(".filter-bar").first();
}

async function expectCompactFilterBar(filterBar: Locator): Promise<void> {
  await expect(filterBar.locator(".compact-filter-menu .filter-btn")).toBeVisible();
  await expect(filterBar.locator(".local-filter-menu .filter-btn")).toBeHidden();
  await expect(filterBar.locator(".state-toggle")).toBeHidden();
  await expect(filterBar.locator(".group-toggle")).toBeHidden();
  await expectFastAnimation(filterBar.locator(".compact-filter-menu"));
}

async function openCompactFilters(filterBar: Locator): Promise<Locator> {
  await filterBar.locator(".compact-filter-menu .filter-btn").click();
  const dropdown = filterBar.page().locator(".filter-dropdown");
  await expect(dropdown).toBeVisible();
  return dropdown;
}

async function expectExpandedFilterBar(filterBar: Locator): Promise<void> {
  await expect(filterBar.locator(".compact-filter-menu .filter-btn")).toBeHidden();
  await expect(filterBar.locator(".state-toggle")).toBeVisible();
  await expect(filterBar.locator(".group-toggle")).toBeVisible();
  await expectFastAnimation(filterBar.locator(".state-toggle"));
  const filterMetrics = await filterBar.evaluate((node) => ({
    clientWidth: node.clientWidth,
    scrollWidth: node.scrollWidth,
  }));
  expect(filterMetrics.scrollWidth).toBeLessThanOrEqual(filterMetrics.clientWidth);
}

async function expectPullLocalFilterIconOnly(filterBar: Locator): Promise<void> {
  const localFilter = filterBar.locator(".local-filter-menu");
  await expect(filterBar.locator(".state-toggle")).toBeVisible();
  await expect(filterBar.locator(".group-toggle")).toBeVisible();
  await expect(filterBar.locator(".compact-filter-menu")).toBeHidden();
  await expect(localFilter.locator(".filter-trigger-label")).toHaveCSS("display", "none");

  const triggerWidth = await localFilter
    .locator(".filter-btn")
    .evaluate((node) => Math.round(node.getBoundingClientRect().width));
  expect(triggerWidth).toBe(34);

  const filterMetrics = await filterBar.evaluate((node) => ({
    clientWidth: node.clientWidth,
    scrollWidth: node.scrollWidth,
  }));
  expect(filterMetrics.scrollWidth).toBeLessThanOrEqual(filterMetrics.clientWidth);
}

async function expectPullLocalFilterLabeled(filterBar: Locator): Promise<void> {
  await expectExpandedFilterBar(filterBar);
  const localFilter = filterBar.locator(".local-filter-menu");
  await expect(localFilter.locator(".filter-trigger-label")).toHaveText("PR filters");
  await expect(localFilter.locator(".filter-trigger-label")).not.toHaveCSS("display", "none");
}

async function expectFastAnimation(locator: Locator): Promise<void> {
  const durationMs = await locator.evaluate((node) => {
    const durations = getComputedStyle(node)
      .animationDuration.split(",")
      .map((value) => value.trim())
      .filter(Boolean)
      .map((value) => (value.endsWith("ms") ? Number.parseFloat(value) : Number.parseFloat(value) * 1000));
    return Math.max(...durations);
  });
  expect(durationMs).toBeGreaterThan(0);
  expect(durationMs).toBeLessThanOrEqual(150);
}

test.describe("collapsible sidebar", () => {
  test("collapse and expand via strip on pulls", async ({ page }) => {
    await page.goto("/pulls");
    await waitForPRList(page);

    const sidebar = page.locator(".sidebar");
    await expect(sidebar).toBeVisible();
    await expect(sidebar).not.toHaveClass(/sidebar--collapsed/);

    await collapseToggle(sidebar).click();
    await expect(sidebar).toHaveClass(/sidebar--collapsed/);

    const expandBtn = expandToggle(sidebar);
    await expect(expandBtn).toBeVisible();

    await expandBtn.click();
    await expect(sidebar).not.toHaveClass(/sidebar--collapsed/);
  });

  test("collapse and expand via strip on issues", async ({ page }) => {
    await page.goto("/issues");
    await waitForIssueList(page);

    const sidebar = page.locator(".sidebar");
    await expect(sidebar).toBeVisible();
    await expect(sidebar).not.toHaveClass(/sidebar--collapsed/);

    await collapseToggle(sidebar).click();
    await expect(sidebar).toHaveClass(/sidebar--collapsed/);

    const expandBtn = expandToggle(sidebar);
    await expect(expandBtn).toBeVisible();

    await expandBtn.click();
    await expect(sidebar).not.toHaveClass(/sidebar--collapsed/);
  });

  test("header expand on non-list route after collapsing", async ({ page }) => {
    await page.goto("/pulls");
    await waitForPRList(page);

    const sidebar = page.locator(".sidebar");
    await collapseToggle(sidebar).click();
    await expect(sidebar).toHaveClass(/sidebar--collapsed/);

    // Navigate to board view (no sidebar strip).
    await page.goto("/pulls/board");
    await expect(page).toHaveURL(/\/pulls\/board$/);

    // Header expand button should be visible.
    const headerToggle = page.getByRole("button", {
      name: "Expand sidebar",
    });
    await expect(headerToggle).toBeVisible();

    // Click it to expand.
    await headerToggle.click();

    // Navigate back to list view and verify sidebar is expanded.
    await page.goto("/pulls");
    await waitForPRList(page);
    const restoredSidebar = page.locator(".sidebar");
    await expect(restoredSidebar).not.toHaveClass(/sidebar--collapsed/);
  });

  test("keyboard shortcut toggles sidebar", async ({ page }) => {
    await page.goto("/pulls");
    await waitForPRList(page);

    const sidebar = page.locator(".sidebar");
    await expect(sidebar).not.toHaveClass(/sidebar--collapsed/);

    // Press Cmd+[ (macOS) or Ctrl+[ to collapse.
    const modifier = process.platform === "darwin" ? "Meta" : "Control";
    await page.keyboard.press(`${modifier}+[`);
    await expect(sidebar).toHaveClass(/sidebar--collapsed/);

    // Press again to expand.
    await page.keyboard.press(`${modifier}+[`);
    await expect(sidebar).not.toHaveClass(/sidebar--collapsed/);
  });

  test("sidebar can be resized on pulls and keeps the new width after reload", async ({ page }) => {
    await expectResizedSidebar(page, "/pulls", waitForPRList);
  });

  test("sidebar can be resized on issues and keeps the new width after reload", async ({ page }) => {
    await expectResizedSidebar(page, "/issues", waitForIssueList);
  });

  test("pull filters collapse into a compact menu when sidebar is tight", async ({ page }) => {
    await expectCompactFiltersAtMinimumWidth(page, "/pulls", waitForPRList);
  });

  test("issue filters collapse into a compact menu when sidebar is tight", async ({ page }) => {
    await expectCompactFiltersAtMinimumWidth(page, "/issues", waitForIssueList);
  });

  test("pull filters stay compact when a narrow viewport sidebar is opened", async ({ page }) => {
    await expectCompactFiltersInNarrowViewport(page, "/pulls", waitForPRList);
  });

  test("issue filters stay compact when a narrow viewport sidebar is opened", async ({ page }) => {
    await expectCompactFiltersInNarrowViewport(page, "/issues", waitForIssueList);
  });

  test("pull filters switch at the buffered 431px fit point", async ({ page }) => {
    await expectCompactFilterBar(await setPersistedSidebarWidth(page, "/pulls", 430, waitForPRList));
    await expectExpandedFilterBar(await setPersistedSidebarWidth(page, "/pulls", 431, waitForPRList));
  });

  test("pull PR filter becomes icon-only before the filter row compacts", async ({ page }) => {
    await expectPullLocalFilterIconOnly(await setPersistedSidebarWidth(page, "/pulls", 520, waitForPRList));
    await expectPullLocalFilterLabeled(await setPersistedSidebarWidth(page, "/pulls", 521, waitForPRList));
  });

  test("issue filters switch at the buffered 373px fit point", async ({ page }) => {
    await expectCompactFilterBar(await setPersistedSidebarWidth(page, "/issues", 372, waitForIssueList));
    await expectExpandedFilterBar(await setPersistedSidebarWidth(page, "/issues", 373, waitForIssueList));
  });

  test("pull compact filters update state and grouping", async ({ page }) => {
    const filterBar = await setPersistedSidebarWidth(page, "/pulls", 395, waitForPRList);
    await expectCompactFilterBar(filterBar);

    const dropdown = await openCompactFilters(filterBar);
    await expect(dropdown.locator(".filter-section-title", { hasText: "PR" })).toBeVisible();
    await expect(dropdown.locator(".filter-section-title", { hasText: "Kanban" })).toBeVisible();
    await dropdown.locator(".filter-item", { hasText: "Closed" }).click();
    await expect(filterBar.page().locator(".state-note")).toBeVisible();
    await expect(dropdown).toBeVisible();

    await dropdown.locator(".filter-item", { hasText: "Flat list" }).click();
    await expect(dropdown).toBeVisible();
    await expect(page.locator(".repo-header")).toHaveCount(0, {
      timeout: 5_000,
    });
    await expect(page.locator(".repo-chip").first()).toBeVisible();
  });

  test("pull compact filters apply PR attributes and kanban status against the real API", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const stateResponse = await page.request.put(`${server.info.base_url}/api/v1/pulls/github/acme/widgets/1/state`, {
        data: { status: "reviewing" },
      });
      expect(stateResponse.ok()).toBe(true);

      const pullsResponse = await page.request.get(`${server.info.base_url}/api/v1/pulls?state=open`);
      expect(pullsResponse.ok()).toBe(true);
      const pulls = (await pullsResponse.json()) as PullListRow[];
      const expectedTitles = pulls
        .filter((pull) => pull.ReviewDecision.trim().toUpperCase() === "APPROVED" && pull.KanbanStatus === "reviewing")
        .map((pull) => pull.Title);
      expect(expectedTitles).toEqual(["Add widget caching layer"]);

      const filterBar = await setPersistedSidebarWidth(page, `${server.info.base_url}/pulls`, 395, waitForPRList);
      await expectCompactFilterBar(filterBar);

      const dropdown = await openCompactFilters(filterBar);
      await dropdown.getByRole("button", { name: "Approved", exact: true }).click();
      await dropdown.getByRole("button", { name: "Reviewing", exact: true }).click();

      const rows = page.locator(".pull-item");
      await expect(rows).toHaveCount(expectedTitles.length);
      for (const title of expectedTitles) {
        await expect(page.locator(".pull-item", { hasText: title })).toBeVisible();
      }
    } finally {
      await server.stop();
    }
  });

  test("pull compact filters keep default-new PRs visible through the real API", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const detailResponse = await page.request.get(`${server.info.base_url}/api/v1/pulls/github/acme/widgets/1`);
      expect(detailResponse.ok()).toBe(true);
      const detail = (await detailResponse.json()) as PullDetailRow;
      expect(detail.merge_request.KanbanStatus).toBe("new");

      const pullsResponse = await page.request.get(`${server.info.base_url}/api/v1/pulls?state=open`);
      expect(pullsResponse.ok()).toBe(true);
      const pulls = (await pullsResponse.json()) as PullListRow[];
      const expectedTitles = pulls.filter((pull) => pull.KanbanStatus === "new").map((pull) => pull.Title);
      expect(expectedTitles).toContain("Add widget caching layer");

      const filterBar = await setPersistedSidebarWidth(page, `${server.info.base_url}/pulls`, 395, waitForPRList);
      await expectCompactFilterBar(filterBar);

      const dropdown = await openCompactFilters(filterBar);
      await dropdown.getByRole("button", { name: "New", exact: true }).click();

      const rows = page.locator(".pull-item");
      await expect(rows).toHaveCount(expectedTitles.length);
      for (const title of expectedTitles) {
        await expect(page.locator(".pull-item", { hasText: title })).toBeVisible();
      }
    } finally {
      await server.stop();
    }
  });

  test("issue compact filters update state and grouping", async ({ page }) => {
    const filterBar = await setPersistedSidebarWidth(page, "/issues", 372, waitForIssueList);
    await expectCompactFilterBar(filterBar);

    const dropdown = await openCompactFilters(filterBar);
    await dropdown.locator(".filter-item", { hasText: "Closed" }).click();
    await expect(filterBar.page().locator(".state-note")).toBeVisible();
    await expect(dropdown).toBeVisible();

    await dropdown.locator(".filter-item", { hasText: "All" }).last().click();
    await expect(dropdown).toBeVisible();
    await expect(page.locator(".repo-header")).toHaveCount(0, {
      timeout: 5_000,
    });
    await expect(page.locator(".repo-chip").first()).toBeVisible();
  });
});
