import { expect, test, type Page } from "@playwright/test";

// Full-stack coverage (real HTTP API + SQLite) for the threaded view's
// sticky column header and per-section sticky repo headers. The shared e2e
// server seeds two repos (acme/widgets, acme/tools) in 7d, so grouped
// threaded mode has multiple sections that the scroll behavior can exercise.

async function openActivityViewDropdown(page: Page) {
  const dropdown = page.locator(".activity-feed .filter-dropdown");
  if (await dropdown.isVisible()) {
    return dropdown;
  }
  await page.locator(".activity-feed .filter-btn", { hasText: "View" }).click();
  await expect(dropdown).toBeVisible();
  return dropdown;
}

async function selectActivityViewItem(page: Page, label: string): Promise<void> {
  const dropdown = await openActivityViewDropdown(page);
  await dropdown.locator(".filter-item", { hasText: label }).click();
}

async function gotoThreadedGrouped(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator(".activity-table .activity-row").first()
    .waitFor({ state: "visible", timeout: 10_000 });
  await selectActivityViewItem(page, "Threaded");
  await page.keyboard.press("Escape");
  await page.locator(".threaded-view .repo-header").first()
    .waitFor({ state: "visible", timeout: 10_000 });
}

test.describe("threaded activity sticky headers", () => {
  test("column header stays visible after scrolling and aligns with later sections", async ({
    page,
  }) => {
    await gotoThreadedGrouped(page);

    const columnHeaders = page.locator(".threaded-view .activity-column-headers");
    await expect(columnHeaders).toBeVisible();
    const beforeBox = await columnHeaders.boundingBox();
    expect(beforeBox).not.toBeNull();
    const beforeTop = beforeBox!.y;

    // Scroll the view far enough that the first section is out of view
    // and a later section is in view; the sticky column header must remain
    // at (or near) its original top y to stay in viewport.
    await page.evaluate(() => {
      const view = document.querySelector(".threaded-view");
      if (view) view.scrollTop = 500;
    });

    await expect(columnHeaders).toBeVisible();
    const afterBox = await columnHeaders.boundingBox();
    expect(afterBox).not.toBeNull();
    // Sticky behavior keeps the column header at the same viewport y.
    expect(Math.abs(afterBox!.y - beforeTop)).toBeLessThan(4);

    // The first column header cell ("Type") must line up with the type
    // chips in the later section's rows. After scrolling, find a PR/Issue
    // chip inside an .item-row that's in the viewport and compare x-edges.
    const typeHeader = columnHeaders.locator(".cell--type");
    const headerX = (await typeHeader.boundingBox())!.x;
    const firstVisibleItemChip = page
      .locator(".threaded-view .item-row .cell--type :is(.chip--kind-pr, .chip--kind-issue)")
      .first();
    await expect(firstVisibleItemChip).toBeVisible();
    const chipX = (await firstVisibleItemChip.boundingBox())!.x;
    // Allow ~2px sub-pixel slack; large mismatches would mean each section
    // has its own column tracks again (the regression we are guarding against).
    expect(Math.abs(chipX - headerX)).toBeLessThan(4);
  });

  test("each repo header is bounded to its own section instead of stacking", async ({
    page,
  }) => {
    await gotoThreadedGrouped(page);

    const repoHeaders = page.locator(".threaded-view .repo-header");
    const count = await repoHeaders.count();
    // The seed produces at least acme/widgets and acme/tools sections.
    expect(count).toBeGreaterThanOrEqual(2);

    // Scroll into the second section so it has the chance to stick.
    await page.evaluate(() => {
      const view = document.querySelector(".threaded-view");
      if (view) view.scrollTop = 400;
    });

    // After scrolling mid-list, only ONE repo header can be "stuck" at the
    // sticky top. If sections were display: contents, two sticky headers
    // would both pin at the same y and visibly overlap.
    const boxes = await Promise.all(
      Array.from({ length: count }, (_, i) =>
        repoHeaders.nth(i).boundingBox(),
      ),
    );
    const yValues = boxes
      .filter((b): b is NonNullable<typeof b> => b !== null)
      .map((b) => Math.round(b.y));
    const distinct = new Set(yValues);
    expect(distinct.size).toBe(yValues.length);
  });
});
