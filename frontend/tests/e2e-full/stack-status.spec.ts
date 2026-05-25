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

test("stack member navigation preserves the focus route with full-stack data", async ({ page }) => {
  await page.goto("/focus/pulls/github/acme/tools/11");

  const detail = page.locator(".pull-detail");
  await expect(detail).toBeVisible();

  await detail.getByTestId("stack-chip").click();
  await detail.getByRole("button", { name: "#10 Auth: extract token refresh helper" }).click();

  await expect(page).toHaveURL(/\/focus\/pulls\/github\/acme\/tools\/10$/);
  await expect(detail.locator(".stack-panel")).toContainText("3 PRs · current 1/3");
});

test("stack member navigation updates the activity drawer with full-stack data", async ({ page }) => {
  await page.goto(
    "/?selected=pr:11&provider=github&platform_host=github.com&repo_path=acme%2Ftools",
  );

  const detail = page.locator(".activity-detail");
  await expect(detail).toBeVisible();
  await expect(detail.locator(".pull-detail")).toBeVisible();

  await detail.getByTestId("stack-chip").click();
  await detail.getByRole("button", { name: "#10 Auth: extract token refresh helper" }).click();

  await expect(page).toHaveURL(/selected=pr%3A10/);
  await expect(page).toHaveURL(/repo_path=acme%2Ftools/);
  await expect(detail.locator(".activity-detail-header")).toContainText("acme/tools#10");
  await expect(detail.locator(".stack-panel")).toContainText("3 PRs · current 1/3");
});
