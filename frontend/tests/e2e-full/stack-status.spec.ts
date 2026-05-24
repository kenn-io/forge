import { expect, test } from "@playwright/test";

test("stack status renders a passive base row from the full-stack API", async ({ page }) => {
  await page.goto("/pulls/github/acme/tools/11");

  const detail = page.locator(".pull-detail");
  await expect(detail).toBeVisible();

  await detail.getByTestId("stack-chip").click();

  const panel = detail.locator(".stack-panel");
  await expect(panel).toContainText("3 PRs · current 2/3");
  await expect(panel.locator(".stack-member-link")).toHaveText([
    "#12 Auth: error handling UI",
    "#11 Auth: add retry with backoff",
    "#10 Auth: extract token refresh helper",
  ]);

  const baseRow = panel.locator(".stack-row--base");
  await expect(baseRow).toBeVisible();
  await expect(baseRow).toHaveAttribute("aria-label", "Stack base main");
  await expect(baseRow.locator(".stack-base-name")).toHaveText("main");
  await expect(baseRow.locator(".stack-member-link")).toHaveCount(0);
  await expect(page).toHaveURL(/\/pulls\/github\/acme\/tools\/11$/);
});
