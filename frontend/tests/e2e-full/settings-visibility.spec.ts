import { expect, test, type Page } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

// Hide/show is a settings-owned preference stored against the repository's
// stable catalog identity. This walks the workflow through the real backend:
// hiding via the row gear menu must drop the repository from interactive
// catalogs and release a persisted global filter scoped to it, survive a
// reload, and come back through "Show in UI".

let server: IsolatedE2EServer | undefined;

test.beforeEach(async () => {
  server = await startIsolatedE2EServer();
});

test.afterEach(async () => {
  await server?.stop();
  server = undefined;
});

async function toggleVisibility(page: Page, item: "Hide from UI" | "Show in UI"): Promise<void> {
  const row = page.locator(".repo-row", { hasText: "acme/widgets" });
  await expect(row).toBeVisible();
  const saveResponsePromise = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/repo/github/acme/widgets/ui-visibility") &&
      response.request().method() === "PUT",
  );
  await row.getByRole("button", { name: "Configure acme/widgets" }).click();
  await row.getByRole("menuitem", { name: item }).click();
  const saveResponse = await saveResponsePromise;
  const saveBody = await saveResponse.text();
  expect(saveResponse.status(), `PUT ui-visibility failed: ${saveBody}`).toBe(200);
}

test("hiding a repository empties interactive catalogs and its filter until shown again", async ({ page }) => {
  await page.goto(`${server!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  // Scope the persisted global repo filter to the repository about to be
  // hidden, so hiding also has to release the stale selection.
  await page.evaluate(() => {
    localStorage.setItem("kenn-forge-filter-repo", "github|github.com/acme/widgets");
  });

  await toggleVisibility(page, "Hide from UI");

  // Settings stays the unfiltered management surface.
  await expect(page.locator(".repo-row", { hasText: "acme/widgets" })).toBeVisible();

  await page.goto(`${server!.info.base_url}/repos`);
  const widgetsCard = page.locator(".repo-card").filter({
    has: page.getByRole("button", { name: /acme\s*\/\s*widgets/ }),
  });
  const toolsCard = page.locator(".repo-card").filter({
    has: page.getByRole("button", { name: /acme\s*\/\s*tools/ }),
  });
  await expect(toolsCard.first()).toBeVisible();
  await expect(widgetsCard).toHaveCount(0);

  // The persisted filter selection normalizes away instead of silently
  // keeping lists scoped to the hidden repository.
  await page.goto(`${server!.info.base_url}/issues`);
  await expect(page.getByRole("button", { name: /^Select repository:/ })).toContainText("Global");

  // The preference survives a reload and reverses through the same menu.
  await page.goto(`${server!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
  await toggleVisibility(page, "Show in UI");

  await page.goto(`${server!.info.base_url}/repos`);
  await expect(widgetsCard.first()).toBeVisible();
});
