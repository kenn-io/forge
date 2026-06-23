import { expect, test } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer";

test.describe("repository source browser", () => {
  test("opens a seeded repository through the real browser API", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.addInitScript(() => {
        localStorage.setItem("repo-browser-view-mode", "preview");
      });

      const blobLoaded = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "GET" &&
          url.pathname === "/api/v1/repo/github/acme/widgets/browser/blob" &&
          url.searchParams.get("path") === "README.md" &&
          response.ok()
        );
      });

      await page.goto(`${server.info.base_url}/repo/browser?provider=github&repo_path=acme%2Fwidgets&path=README.md`);
      await blobLoaded;

      const browser = page.getByRole("region", { name: "Repository source browser" });
      await expect(browser).toBeVisible();
      await expect(browser.locator(".repo-browser__repo")).toHaveText("acme/widgets");
      await expect(browser.locator(".repo-browser__ref")).toHaveText("main");
      await expect(browser.locator(".repo-browser__tree")).toContainText("handler");

      const viewer = browser.getByRole("main", { name: "Selected file" });
      await expect(viewer.locator(".repo-browser__path")).toContainText("README.md");
      await expect(viewer.locator(".repo-browser__source")).toContainText("# Widget Service");
      await expect(viewer.locator(".repo-browser__markdown")).toHaveCount(0);
      await expect(page).not.toHaveURL(/mode=preview/);

      await browser.getByRole("button", { name: "Preview" }).click();
      await expect(viewer.locator(".repo-browser__markdown h1")).toHaveText("Widget Service");
      await expect(viewer.locator(".repo-browser__source")).toHaveCount(0);
      await expect(page).toHaveURL(/mode=preview/);

      const history = browser.getByRole("complementary", { name: "File history" });
      await expect(history).toContainText("Initial commit");
      await history.getByRole("button", { name: /Initial commit/ }).click();
      await expect(history.locator(".repo-browser__commit-detail")).toContainText("Initial commit");
    } finally {
      await server.stop();
    }
  });
});
