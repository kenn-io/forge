import { expect, test, type Page } from "@playwright/test";

// Full-stack coverage (real HTTP API + SQLite). The shared e2e server seeds the
// Activity config with view_mode "flat" and 7d activity data that includes
// comment/review events, so switching to threaded mode renders item rows with
// nested event rows that the collapse controls can hide and show.

async function openActivityViewDropdown(page: Page) {
  const dropdown = page.locator(".activity-feed .filter-dropdown");
  if (await dropdown.isVisible()) {
    return dropdown;
  }
  await page.locator(".activity-feed .filter-btn", { hasText: "View" }).click();
  await expect(dropdown).toBeVisible();
  return dropdown;
}

async function selectActivityViewItem(
  page: Page,
  label: string,
): Promise<void> {
  const dropdown = await openActivityViewDropdown(page);
  await dropdown.locator(".filter-item", { hasText: label }).click();
}

async function gotoThreadedActivity(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator(".activity-table tbody .activity-row").first()
    .waitFor({ state: "visible", timeout: 10_000 });
  await selectActivityViewItem(page, "Threaded");
  await page.locator(".threaded-view .item-row").first()
    .waitFor({ state: "visible", timeout: 10_000 });
}

test.describe("threaded activity collapse", () => {
  test("collapse all hides every event row and expand all restores them", async ({
    page,
  }) => {
    await gotoThreadedActivity(page);

    const eventRows = page.locator(".threaded-view .event-row");
    await expect(eventRows.first()).toBeVisible();
    const initialCount = await eventRows.count();
    expect(initialCount).toBeGreaterThan(0);

    await page.getByRole("button", { name: "Collapse all" }).click();
    await expect(eventRows).toHaveCount(0);
    await expect(page.locator(".threaded-view .item-row").first())
      .toBeVisible();

    // The control flips to Expand all; clicking it brings every event back.
    await page.getByRole("button", { name: "Expand all" }).click();
    await expect(eventRows).toHaveCount(initialCount);
  });

  test("a single caret expands only its own item after collapse all", async ({
    page,
  }) => {
    await gotoThreadedActivity(page);

    const eventRows = page.locator(".threaded-view .event-row");
    const fullCount = await eventRows.count();
    expect(fullCount).toBeGreaterThan(1);

    await page.getByRole("button", { name: "Collapse all" }).click();
    await expect(eventRows).toHaveCount(0);

    await page.locator(".threaded-view .item-row").first()
      .locator(".thread-caret").click();

    // Only the clicked item's events reappear; the rest stay collapsed.
    await expect(eventRows.first()).toBeVisible();
    const partial = await eventRows.count();
    expect(partial).toBeGreaterThan(0);
    expect(partial).toBeLessThan(fullCount);
  });
});
