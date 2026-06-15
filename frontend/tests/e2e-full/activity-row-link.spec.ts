import { expect, test, type Page } from "@playwright/test";

// Full-stack coverage that flat and threaded activity rows still expose a
// "Open activity" affordance that jumps directly to the provider URL
// without going through the in-app drawer. Regression guard for the
// dropped link column during the flat/threaded layout unification.

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

async function captureWindowOpen(page: Page): Promise<() => Promise<string | null>> {
  // Intercept window.open before the click handler runs so we can assert
  // exactly what URL the row's link button would have opened.
  await page.evaluate(() => {
    delete (globalThis as unknown as { __lastOpen?: string }).__lastOpen;
    window.open = ((url: string) => {
      (globalThis as unknown as { __lastOpen?: string }).__lastOpen = typeof url === "string" ? url : String(url);
      return null;
    }) as typeof window.open;
  });
  return () => page.evaluate(() => (globalThis as unknown as { __lastOpen?: string }).__lastOpen ?? null);
}

test.describe("activity row link button", () => {
  test("flat row link button opens activity URL without triggering row select", async ({ page }) => {
    await page.goto("/");
    const firstRow = page.locator(".activity-table .activity-row").first();
    await firstRow.waitFor({ state: "visible", timeout: 10_000 });

    const lastOpen = await captureWindowOpen(page);

    const linkBtn = firstRow.locator(".link-btn");
    await expect(linkBtn).toBeVisible();
    await linkBtn.click();

    const opened = await lastOpen();
    expect(opened).toBeTruthy();
    // Seed activity events all target github.com/acme/... URLs.
    expect(opened).toContain("github.com/acme/");

    // The detail drawer must not have opened (row click was suppressed).
    await expect(page.locator(".activity-drawer")).toHaveCount(0);
  });

  test("threaded item row link button opens item URL without expanding the item", async ({ page }) => {
    await page.goto("/");
    await page.locator(".activity-table .activity-row").first().waitFor({ state: "visible", timeout: 10_000 });
    await selectActivityViewItem(page, "Threaded");
    await page.keyboard.press("Escape");
    const itemRow = page.locator(".threaded-view .item-row:not(.branch-activity-row)").first();
    await itemRow.waitFor({ state: "visible", timeout: 10_000 });

    const lastOpen = await captureWindowOpen(page);

    const linkBtn = itemRow.locator(".link-btn");
    await expect(linkBtn).toBeVisible();
    await linkBtn.click();

    const opened = await lastOpen();
    expect(opened).toBeTruthy();
    // Threaded item-rows link to the underlying item URL (PR or issue).
    expect(opened).toMatch(/github\.com\/acme\/[^/]+\/(pull|issues)\/\d+/);

    // The detail drawer must not have opened (caret/row click suppressed).
    await expect(page.locator(".activity-drawer")).toHaveCount(0);
  });

  test("threaded activity rows remain clickable after the split detail pane opens", async ({ page }) => {
    await page.setViewportSize({ width: 900, height: 720 });
    await page.addInitScript(() => {
      localStorage.setItem("middleman-activity-pane-width", "1200");
    });
    await page.goto("/");
    const flatRows = page.locator(".activity-table .activity-row");
    await flatRows.first().waitFor({ state: "visible", timeout: 10_000 });
    await flatRows.first().click();
    const detailHeader = page.locator(".activity-detail-header");
    await expect(detailHeader).toBeVisible();
    const firstSelection = await detailHeader.textContent();
    expect(firstSelection).toBeTruthy();

    await selectActivityViewItem(page, "Threaded");
    await expect(page.locator(".activity-feed .filter-dropdown")).toBeHidden();

    const itemRows = page.locator(".threaded-view .item-row:not(.branch-activity-row)");
    await expect(itemRows.nth(1)).toBeVisible();

    await itemRows.nth(1).click();
    await expect(detailHeader).not.toHaveText(firstSelection!);
  });

  test("grouped threaded compact rows keep the expand chevron separate from the type chip", async ({ page }) => {
    await page.setViewportSize({ width: 900, height: 720 });
    await page.goto("/");
    const flatRows = page.locator(".activity-table .activity-row");
    await flatRows.first().waitFor({ state: "visible", timeout: 10_000 });
    await flatRows.first().click();

    await selectActivityViewItem(page, "Threaded");
    await expect(page.locator(".activity-feed .filter-dropdown")).toBeHidden();

    const itemRow = page.locator(".threaded-view .item-row:not(.branch-activity-row)").first();
    const caret = itemRow.locator(".thread-caret");
    const typeCell = itemRow.locator(".cell--type");
    await expect(caret).toBeVisible();
    await expect(typeCell).toBeVisible();

    const [caretBox, typeBox] = await Promise.all([caret.boundingBox(), typeCell.boundingBox()]);
    expect(caretBox).not.toBeNull();
    expect(typeBox).not.toBeNull();
    expect(typeBox!.x - (caretBox!.x + caretBox!.width)).toBeGreaterThanOrEqual(8);
  });
});
