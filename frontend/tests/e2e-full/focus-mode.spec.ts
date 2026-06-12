import { expect, test } from "@playwright/test";

test.describe("focus mode", () => {
  test("PR focus route renders detail without shell chrome", async ({ page }) => {
    await page.goto("/focus/pulls/github/acme/widgets/1");
    await page.locator(".focus-layout").waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".pull-detail")).toBeVisible();
    await expect(page.locator(".app-header")).not.toBeAttached();
    await expect(page.locator(".sidebar")).not.toBeAttached();
    await expect(page.locator(".status-bar")).not.toBeAttached();
  });

  test("issue focus route renders detail without shell chrome", async ({ page }) => {
    await page.goto("/focus/issues/github/acme/widgets/10");
    await page.locator(".focus-layout").waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".issue-detail")).toBeVisible();
    await expect(page.locator(".app-header")).not.toBeAttached();
    await expect(page.locator(".sidebar")).not.toBeAttached();
    await expect(page.locator(".status-bar")).not.toBeAttached();
  });

  test("narrow PR focus route shows actions only inside the actions menu", async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 720 });
    await page.goto("/focus/pulls/github/acme/widgets/1");
    await page.locator(".focus-layout .pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".actions-row--primary")).toBeHidden();
    await expect(page.locator(".label-editor-anchor--inline")).toBeHidden();
    await expect(page.locator(".actions-row--workspace")).toBeHidden();
    await expect(page.locator(".actions-menu-trigger")).toBeVisible();

    await page.locator(".actions-menu-trigger").click();

    await expect(page.locator(".actions-row--primary .btn--approve")).toBeHidden();
    await expect(page.locator(".actions-menu-popover .btn--approve")).toHaveCount(0);
    await expect(page.locator(".actions-menu-popover .btn--merge")).toBeVisible();
    await expect(page.locator(".actions-menu-popover .btn--close")).toBeVisible();
    await expect(page.locator(".actions-menu-popover").getByRole("button", { name: "Labels" })).toBeVisible();
    await expect(page.locator(".actions-menu-popover .btn--workspace")).toBeVisible();

    await page.locator(".actions-menu-popover").getByRole("button", { name: "Labels" }).click();
    await expect(page.locator(".label-picker")).toBeVisible();
    await expect(page.locator(".actions-menu-popover")).toBeHidden();
    await expect(page.locator(".label-editor-backdrop")).toBeVisible();
    await expect(page.locator(".label-editor-backdrop")).toHaveCSS("background-color", "rgba(128, 128, 128, 0.3)");

    const pickerRect = await page.locator(".label-picker").boundingBox();
    const viewport = page.viewportSize();
    expect(pickerRect).not.toBeNull();
    expect(viewport).not.toBeNull();
    if (pickerRect && viewport) {
      expect(pickerRect.y).toBeGreaterThanOrEqual(20);
      expect(pickerRect.y + pickerRect.height).toBeLessThanOrEqual(viewport.height - 20);
      expect(Math.abs(pickerRect.x + pickerRect.width / 2 - viewport.width / 2)).toBeLessThanOrEqual(2);
    }

    await page.mouse.click(4, 4);
    await expect(page.locator(".label-picker")).toBeHidden();
  });

  test("narrow PR focus route closes actions menu when Labels toggles an open picker", async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 720 });
    await page.goto("/focus/pulls/github/acme/widgets/1");
    await page.locator(".focus-layout .pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    await page.locator(".actions-menu-trigger").click();
    await page.locator(".actions-menu-popover").getByRole("button", { name: "Labels" }).click();
    await expect(page.locator(".label-picker")).toBeVisible();
    await expect(page.locator(".actions-menu-popover")).toBeHidden();

    await page.locator(".actions-menu-trigger").click();
    await expect(page.locator(".actions-menu-popover")).toBeVisible();

    await page.locator(".actions-menu-popover").getByRole("button", { name: "Labels" }).click();
    await expect(page.locator(".label-picker")).toBeHidden();
    await expect(page.locator(".actions-menu-popover")).toBeHidden();
  });

  test("PR focus route dismisses the inline label picker on outside click", async ({ page }) => {
    await page.setViewportSize({ width: 560, height: 720 });
    await page.goto("/focus/pulls/github/acme/widgets/1");
    await page.locator(".focus-layout .pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    await page.locator(".label-editor-anchor--inline").getByRole("button", { name: "Labels" }).click();
    await expect(page.locator(".label-picker")).toBeVisible();

    await page.mouse.click(4, 4);
    await expect(page.locator(".label-picker")).toBeHidden();
  });

  test("PR focus route keeps workspace inside the mid-narrow action grid", async ({ page }) => {
    await page.route("**/api/v1/workspaces", async (route) => {
      if (route.request().method() !== "POST") {
        await route.continue();
        return;
      }
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({
          title: "Workspace failed",
          detail: "workspace setup failed",
        }),
      });
    });

    await page.setViewportSize({ width: 560, height: 720 });
    await page.goto("/focus/pulls/github/acme/widgets/1");
    await page.locator(".focus-layout .pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".actions-menu-trigger")).toBeHidden();
    await expect(page.locator(".actions-row--primary .primary-workspace-action .btn--workspace")).toBeVisible();
    await expect(page.locator(".actions-row--workspace")).toBeHidden();

    const copyNumberHeight = await page
      .locator(".meta-row .copy-number-btn")
      .evaluate((node) => node.getBoundingClientRect().height);
    expect(copyNumberHeight).toBeLessThan(28);
    await expect(page.locator(".meta-sep--branch")).toBeHidden();
    await expect(page.locator(".meta-sep--sync")).toBeHidden();

    await page.locator(".actions-row--primary .primary-workspace-action .btn--workspace").click();
    await expect(page.locator(".actions-row--primary .primary-workspace-action .action-error")).toHaveText(
      "workspace setup failed",
    );
  });

  test("narrow merged PR focus route keeps labels and workspace actions available", async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 720 });
    await page.goto("/focus/pulls/github/acme/widgets/3");
    await page.locator(".focus-layout .pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".actions-menu-trigger")).toBeHidden();
    await expect(page.locator(".label-editor-anchor--inline").getByRole("button", { name: "Labels" })).toBeVisible();
    await expect(page.locator(".actions-row--workspace .btn--workspace")).toBeVisible();
  });

  test("browser back/forward works between focus routes", async ({ page }) => {
    await page.goto("/focus/pulls/github/acme/widgets/1");
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    // Navigate forward to an issue focus route.
    await page.goto("/focus/issues/github/acme/widgets/10");
    await page.locator(".issue-detail").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page).toHaveURL(/\/focus\/issues\/github\/acme\/widgets\/10$/);

    // Go back to the PR focus route.
    await page.goBack();
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page).toHaveURL(/\/focus\/pulls\/github\/acme\/widgets\/1$/);

    // Go forward to the issue focus route.
    await page.goForward();
    await page.locator(".issue-detail").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page).toHaveURL(/\/focus\/issues\/github\/acme\/widgets\/10$/);
  });
});
