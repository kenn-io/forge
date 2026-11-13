import { expect, test } from "@playwright/test";
import { mockIssueFixture, mockPullFixture } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

for (const kind of ["issues", "pulls"] as const) {
  test(`${kind} page passes its capped All list to the status bar`, async ({ page }) => {
    await mockApi(page);
    const template = kind === "issues" ? mockIssueFixture(7) : mockPullFixture(42);
    const rows = Array.from({ length: 250 }, (_, index) => ({
      ...template,
      ID: index + 1,
      Number: index + 1,
      Title: `Item ${index + 1}`,
      State: index < 200 ? "closed" : "open",
    }));
    for (const endpoint of ["issues", "pulls"]) {
      await page.route(`**/api/v1/${endpoint}**`, async (route) => {
        const url = new URL(route.request().url());
        if (route.request().method() !== "GET" || url.pathname !== `/api/v1/${endpoint}`) {
          await route.fallback();
          return;
        }
        const state = url.searchParams.get("state");
        const filtered =
          endpoint === kind ? rows.filter((row) => !state || state === "all" || row.State === state) : [];
        const limit = Number(url.searchParams.get("limit")) || filtered.length;
        await route.fulfill({ json: filtered.slice(0, limit) });
      });
    }

    await page.goto(`/${kind}`);
    const counts = page.locator(".kit-status-bar__section--left .status-item");

    const trigger = page.locator(".compact-filter-menu .kit-filter-dropdown__btn");
    await trigger.click();
    await page
      .locator(".kit-filter-dropdown__panel")
      .getByRole("button", { name: "All states", exact: true })
      .first()
      .click();
    await trigger.click();
    await expect(counts).toHaveText(
      kind === "issues" ? ["0 PRs", "0+ issues", "0+ repos"] : ["0+ PRs", "0 issues", "0+ repos"],
    );
  });
}
