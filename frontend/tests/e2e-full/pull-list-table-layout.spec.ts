import { expect, test, type Locator, type Page } from "@playwright/test";

// Seed data repos: acme/widgets, acme/tools, with open PRs in both.

async function selectFlatList(page: Page): Promise<void> {
  const flatButton = page.locator(".group-btn", { hasText: "All" });
  if (await flatButton.isVisible()) {
    await flatButton.click();
    return;
  }
  await page.getByRole("button", { name: "Filters" }).click();
  await page.locator(".kit-filter-dropdown__panel .kit-filter-dropdown__item", { hasText: "Flat list" }).click();
  await page.keyboard.press("Escape");
}

async function left(locator: Locator): Promise<number> {
  return (await locator.boundingBox())!.x;
}

async function right(locator: Locator): Promise<number> {
  const box = (await locator.boundingBox())!;
  return box.x + box.width;
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    if (sessionStorage.getItem("kenn-forge:test:pr-table:init") === "1") return;
    localStorage.removeItem("kenn-forge:pullListLayout");
    localStorage.removeItem("kenn-forge:groupingMode");
    sessionStorage.setItem("kenn-forge:test:pr-table:init", "1");
  });
});

test("the PR list layout chosen in Settings shows one-line table rows and survives a reload", async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 });
  await page.goto("/settings?section=settings-appearance");
  await page.getByRole("combobox", { name: "PR list layout: Two-line rows" }).click();
  await page.getByRole("option", { name: "Table, like Activity" }).click();

  await page.goto("/pulls");
  await selectFlatList(page);
  const headers = page.locator(".table-headers");
  await expect(headers).toHaveText(/Repo\s*Author\s*Item\s*When/);

  const row = page.locator(".pull-item--table").filter({ hasText: "acme/tools" }).first();
  await expect(row).toBeVisible();
  // One line per pull request, with each cell under its column header.
  await expect(row.locator(".meta-row")).toHaveCount(0);
  expect(Math.abs((await left(row.locator(".cell--author"))) - (await left(headers.getByText("Author")))) < 1).toBe(
    true,
  );
  expect(Math.abs((await left(row.locator(".cell--title"))) - (await left(headers.getByText("Item")))) < 1).toBe(true);
  const times = page.locator(".pull-item--table .cell--time");
  const timeEdges = await Promise.all((await times.all()).map(right));
  expect(new Set(timeEdges.map(Math.round)).size).toBe(1);
  expect(Math.abs(timeEdges[0]! - (await right(headers.getByText("When")))) < 1).toBe(true);

  await page.reload();
  await expect(page.locator(".pull-item--table").first()).toBeVisible();
  await page.screenshot({ path: "test-results/pull-list-table-layout.png" });

  await page.goto("/settings?section=settings-appearance");
  await page.getByRole("combobox", { name: "PR list layout: Table, like Activity" }).click();
  await page.getByRole("option", { name: "Two-line rows" }).click();
  await page.goto("/pulls");
  await expect(page.locator(".pull-item").first()).toBeVisible();
  await expect(page.locator(".pull-item--table")).toHaveCount(0);
  await expect(page.locator(".table-headers")).toHaveCount(0);
});

test("grouped by repo, the table drops the Repo column", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("kenn-forge:pullListLayout", "table");
    localStorage.setItem("kenn-forge:groupingMode", "byRepo");
  });
  await page.goto("/pulls");
  const headers = page.locator(".table-headers");
  await expect(headers).toHaveText(/^\s*Author\s*Item\s*When\s*$/);
  const row = page.locator(".pull-item--table").first();
  await expect(row).toBeVisible();
  await expect(row.locator(".cell--repo")).toHaveCount(0);
  expect(Math.abs((await left(row.locator(".cell--title"))) - (await left(headers.getByText("Item")))) < 1).toBe(true);
});
