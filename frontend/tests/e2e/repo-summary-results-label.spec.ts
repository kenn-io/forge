import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("results label keeps its width when filters change the count", async ({ page }) => {
  await page.goto("/repos");

  const results = page.locator(".repo-page__results");
  await expect(results).toContainText("12 results");

  const allBox = await results.boundingBox();
  expect(allBox).not.toBeNull();

  const refresh = page.getByRole("button", { name: "Refresh repositories" });
  const refreshBoxAll = await refresh.boundingBox();
  expect(refreshBoxAll).not.toBeNull();

  // "Has PRs" drops the count from two digits to one; the label and its
  // neighbors must not move.
  await page.getByRole("button", { name: "Has PRs", exact: true }).click();
  await expect(results).toContainText("4 results");

  const prsBox = await results.boundingBox();
  expect(prsBox).not.toBeNull();
  expect(prsBox!.width).toBeCloseTo(allBox!.width, 1);
  expect(prsBox!.x).toBeCloseTo(allBox!.x, 1);

  const refreshBoxPrs = await refresh.boundingBox();
  expect(refreshBoxPrs!.x).toBeCloseTo(refreshBoxAll!.x, 1);
});
