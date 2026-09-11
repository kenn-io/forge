import { expect, test } from "@playwright/test";
import { mockApi } from "./support/mockApi";

for (const key of ["projects/theme-rework", "projects/another-worktree", undefined]) {
  test(`active-worktree bootstrap ${key ?? "unset"} reaches the pull sidebar`, async ({ page }) => {
    await mockApi(page);
    await page.addInitScript((activeKey) => {
      if (activeKey !== undefined) window.__kenn_forge_active_worktree_key = activeKey;
    }, key);

    await page.goto("/pulls/github/acme/widgets/55");
    const row = page.locator(".pull-item").filter({ hasText: "Refactor theme system" });
    await expect(row).toBeVisible();
    if (key === "projects/theme-rework") {
      await expect(row).toHaveClass(/active-worktree/);
      await expect(page.locator(".pull-item.active-worktree")).toHaveCount(1);
    } else {
      await expect(page.locator(".pull-item.active-worktree")).toHaveCount(0);
    }
  });
}
