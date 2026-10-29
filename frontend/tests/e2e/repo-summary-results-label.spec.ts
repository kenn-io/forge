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

for (const width of [1440, 390]) {
  test(`the search box is the only edge below the repositories heading at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/repos");

    const search = page.getByPlaceholder("Filter repositories");
    await expect(search).toBeVisible();

    // Horizontal lines drawn between the heading block's last content and the
    // toolbar would stack with the search box's own top border.
    const stray = await page.evaluate(() => {
      const header = document.querySelector(".repo-page__header")!;
      const headingBottom = Math.max(...Array.from(header.children, (child) => child.getBoundingClientRect().bottom));
      const toolbarTop = document.querySelector(".repo-page__search")!.getBoundingClientRect().top;
      const edges: string[] = [];
      for (const element of Array.from(document.querySelectorAll("body *"))) {
        const box = element.getBoundingClientRect();
        if (box.width === 0) continue;
        const style = getComputedStyle(element);
        const sides = [
          ["bottom", box.bottom, style.borderBottomWidth, style.borderBottomStyle],
          ["top", box.top, style.borderTopWidth, style.borderTopStyle],
        ] as const;
        for (const [side, y, lineWidth, lineStyle] of sides) {
          if (parseFloat(lineWidth) > 0 && lineStyle !== "none" && y > headingBottom + 0.5 && y < toolbarTop - 0.5) {
            edges.push(`${element.tagName.toLowerCase()}.${element.className} ${side} at ${y}`);
          }
        }
      }
      return edges;
    });

    expect(stray).toEqual([]);
  });
}
