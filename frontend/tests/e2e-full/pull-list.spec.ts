import { expect, test, type Page } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

// Seeded data summary:
//   open PRs (8): widgets#1, #2, #6, #7, tools#1, tools#10, #11, #12 (last three form a stack)
//   closed/merged PRs (4): widgets#3 (merged), #4 (merged), #5 (closed), tools#2 (merged)

async function waitForPullList(page: Page): Promise<void> {
  // Wait for at least one PR item to appear (data loaded).
  await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function selectPullState(page: Page, label: string): Promise<void> {
  const stateButton = page.locator(".state-btn", { hasText: label });
  if (await stateButton.isVisible()) {
    await stateButton.click();
    return;
  }

  const dropdown = await openCompactFilterMenu(page);
  await dropdown.locator(".kit-filter-dropdown__item", { hasText: label }).first().click();
  await page.keyboard.press("Escape");
  await expect(dropdown).toBeHidden();
}

async function selectPullGrouping(page: Page, label: string): Promise<void> {
  const groupButton = page.locator(".group-btn", { hasText: label });
  if (await groupButton.isVisible()) {
    await groupButton.click();
    return;
  }

  const compactLabel = compactPullGroupingLabel(label);
  const dropdown = await openCompactFilterMenu(page);
  await dropdown.locator(".kit-filter-dropdown__item", { hasText: compactLabel }).click();
  await page.keyboard.press("Escape");
  await expect(dropdown).toBeHidden();
}

function compactPullGroupingLabel(label: string): string {
  if (label === "Repo") return "By repo";
  if (label === "Status") return "By status";
  if (label === "All") return "Flat list";
  return label;
}

async function openCompactFilterMenu(page: Page) {
  const dropdown = page.locator(".kit-filter-dropdown__panel");
  if (!(await dropdown.isVisible())) {
    await page.locator(".compact-filter-menu .kit-filter-dropdown__btn").click();
    await expect(dropdown).toBeVisible();
  }
  return dropdown;
}

const longRepoName = "widgets-with-an-extremely-long-repository-name";
const longRepoPath = `acme/${longRepoName}`;

async function mockLongPullRepoSlug(page: Page): Promise<void> {
  await page.route(
    (url) => url.pathname.endsWith("/api/v1/pulls") && url.searchParams.get("state") === "open",
    async (route) => {
      const response = await route.fetch();
      const pulls = (await response.json()) as Array<{
        repo?: { owner?: string; name?: string; repo_path?: string };
        repo_owner?: string;
        repo_name?: string;
      }>;
      const firstPull = pulls[0];
      if (firstPull) {
        firstPull.repo_owner = "acme";
        firstPull.repo_name = longRepoName;
        if (firstPull.repo) {
          firstPull.repo.owner = "acme";
          firstPull.repo.name = longRepoName;
          firstPull.repo.repo_path = longRepoPath;
        }
      }
      await route.fulfill({ response, json: pulls });
    },
  );
}

async function expectRepoNameToClipSafely(
  item: ReturnType<Page["locator"]>,
  repoName: ReturnType<Page["locator"]>,
  expectedRepoPath: string,
): Promise<void> {
  await item.evaluate((node) => {
    (node as HTMLElement).style.width = "180px";
  });

  await expect(repoName).toHaveText(expectedRepoPath);
  await expect(repoName).toHaveCSS("overflow", "hidden");
  await expect(repoName).toHaveCSS("text-overflow", "ellipsis");
  await expect(repoName).toHaveAttribute("title", expectedRepoPath);

  const nameBox = await repoName.boundingBox();
  const itemBox = await item.boundingBox();
  expect(nameBox).not.toBeNull();
  expect(itemBox).not.toBeNull();
  if (nameBox !== null && itemBox !== null) {
    expect(nameBox.x + nameBox.width).toBeLessThanOrEqual(itemBox.x + itemBox.width + 1);
  }

  const labelOverflow = await repoName.evaluate((node) => ({
    clientWidth: (node as HTMLElement).clientWidth,
    scrollWidth: (node as HTMLElement).scrollWidth,
  }));
  expect(labelOverflow.scrollWidth).toBeGreaterThanOrEqual(labelOverflow.clientWidth);
}

async function expectPullReviewIndicator(page: Page, title: string, label: string): Promise<void> {
  const item = page.locator(".pull-item", { hasText: title });
  await expect(item.locator(`[aria-label='${label}']`)).toBeVisible();
}

test.describe("PR list view", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/pulls");
    await waitForPullList(page);
  });

  test("closed state shows closed and merged PRs grouped by status", async ({ page }) => {
    await selectPullState(page, "Closed");

    await expect(page.locator(".state-note")).toBeVisible();

    await selectPullGrouping(page, "Status");

    const headers = page.locator(".sidebar-group-header");
    await expect(headers).toHaveCount(1);
    await expect(headers.first().locator(".sidebar-group-header__name")).toHaveText("Closed");
  });

  test("search filters PRs by title", async ({ page }) => {
    const input = page.locator(".search-wrap input");
    await input.fill("caching");

    // Wait for the count badge to reflect filtered results. The
    // matching item is already visible in the unfiltered list, so
    // we must wait on a condition that only becomes true after
    // the debounced search request completes.
    await expect(page.locator(".filter-bar .list-count-chip")).toHaveText(/^1 PRs?$/, {
      timeout: 5_000,
    });

    // Verify the single remaining item is the expected one.
    const items = page.locator(".pull-item");
    await expect(items).toHaveCount(1);
    await expect(items.first().locator(".title")).toContainText("caching layer");
  });

  test("PR detail keeps the scrollbar on the pane edge", async ({ page }) => {
    await page.locator(".pull-item").filter({ hasText: "caching layer" }).first().click();

    const pullDetail = page.locator(".pull-detail");
    await expect(pullDetail).toBeVisible();
    // .pull-detail is the content wrapper; the ScrollBox viewport owns
    // vertical scrolling for the conversation pane.
    const scroller = page.getByRole("region", { name: "Pull request conversation" });

    await pullDetail.evaluate((el) => {
      const filler = document.createElement("div");
      filler.style.height = "3000px";
      filler.style.flexShrink = "0";
      filler.style.background = "transparent";
      filler.setAttribute("data-test-filler", "pull-scroll");
      el.appendChild(filler);
    });

    const overflowY = await scroller.evaluate((el) => getComputedStyle(el).overflowY);
    expect(["auto", "scroll"]).toContain(overflowY);

    const before = await scroller.evaluate((el) => ({
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
      scrollTop: el.scrollTop,
    }));
    expect(before.scrollHeight).toBeGreaterThan(before.clientHeight);
    expect(before.scrollTop).toBe(0);

    await scroller.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });

    const finalScroll = await scroller.evaluate((el) => el.scrollTop);
    expect(finalScroll).toBeGreaterThan(0);

    const detailArea = page.locator(".kit-sidebar-layout__main");
    const contentHeader = page.locator(".pull-detail .detail-header");
    const areaBox = await detailArea.boundingBox();
    const detailBox = await scroller.boundingBox();
    const headerBox = await contentHeader.boundingBox();
    expect(areaBox).not.toBeNull();
    expect(detailBox).not.toBeNull();
    expect(headerBox).not.toBeNull();
    if (areaBox !== null && detailBox !== null && headerBox !== null) {
      const scrollportWidth = await scroller.evaluate((el) => el.clientWidth);
      const scrollportCenter = detailBox.x + scrollportWidth / 2;
      const headerCenter = headerBox.x + headerBox.width / 2;
      expect(Math.abs(detailBox.x + detailBox.width - (areaBox.x + areaBox.width))).toBeLessThan(2);
      expect(Math.abs(headerCenter - scrollportCenter)).toBeLessThan(2);
      expect(headerBox.width).toBeLessThanOrEqual(800);
    }
  });

  test("PR detail stale refresh uses the standard syncing indicator", async ({ page }) => {
    await page.route("**/api/v1/pulls/github/acme/widgets/1", async (route) => {
      const response = await route.fetch();
      const detail = (await response.json()) as {
        detail_fetched_at: string;
        merge_request: { UpdatedAt: string };
      };
      detail.detail_fetched_at = "2020-01-01T00:00:00Z";
      detail.merge_request.UpdatedAt = "2026-01-01T00:00:00Z";
      await route.fulfill({ response, json: detail });
    });

    await page.goto("/pulls/github/acme/widgets/1");

    await expect(page.locator(".pull-detail .sync-indicator")).toBeVisible();
    await expect(page.locator(".pull-detail .refresh-banner")).toHaveCount(0);
    await expect(page.getByText("Refreshing...", { exact: true })).toHaveCount(0);
  });
});

test.describe("PR list sidebar", () => {
  let server: IsolatedE2EServer | undefined;

  test.beforeAll(async () => {
    server = await startIsolatedE2EServer();
  });

  test.afterAll(async () => {
    await server?.stop();
  });

  test("sidebar rows show review indicators, clip long repo names, and never show a status chip", async ({ page }) => {
    if (!server) {
      throw new Error("PR list sidebar e2e server was not started");
    }
    await mockLongPullRepoSlug(page);
    await page.goto(`${server.info.base_url}/pulls`);
    await waitForPullList(page);

    await expect(page.locator(".filter-bar .list-count-chip")).toHaveText(/^\d+ PRs$/);
    await selectPullGrouping(page, "All");
    await expectPullReviewIndicator(page, "Add widget caching layer", "PR approved");
    await expectPullReviewIndicator(page, "Fix race condition in event loop", "Changes requested");

    const firstItem = page.locator(".pull-item").first();
    const repoName = firstItem.locator(".repo-name");
    await expect(repoName).toBeVisible();
    await expectRepoNameToClipSafely(firstItem, repoName, longRepoPath);

    // The kanban status chip was removed from the sidebar entirely (the
    // kanban feature itself may go away); no row shows one regardless of
    // its workflow state.
    await expect(page.locator(".pull-item .status-chip")).toHaveCount(0);

    // Compact layout: repo name lives in the meta row, no standalone repo
    // row, and rows keep a uniform two-line height regardless of labels.
    await expect(firstItem.locator(".meta-row .repo-name")).toBeVisible();
    await expect(page.locator(".pull-item .repo-row")).toHaveCount(0);
    await expect(page.locator(".pull-item:has(.label-dot)").first()).toBeVisible();
    const rowHeights = await page
      .locator(".pull-item")
      .evaluateAll((nodes) => nodes.slice(0, 6).map((node) => node.getBoundingClientRect().height));
    expect(rowHeights.length).toBeGreaterThan(1);
    for (const height of rowHeights) {
      expect(height).toBeLessThanOrEqual(60);
      expect(Math.abs(height - (rowHeights[0] ?? 0))).toBeLessThanOrEqual(1);
    }
  });
});
