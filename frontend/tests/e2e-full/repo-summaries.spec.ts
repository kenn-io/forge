import { expect, test, type Page } from "@playwright/test";
import { startIsolatedE2EServer, startIsolatedE2EServerWithOptions } from "./support/e2eServer";

function repoBrowserBlobLoaded(page: Page, path: string) {
  return page.waitForResponse((response) => {
    const url = new URL(response.url());
    return (
      response.request().method() === "GET" &&
      url.pathname === "/api/v1/repo/github/acme/widgets/browser/blob" &&
      url.searchParams.get("path") === path &&
      response.ok()
    );
  });
}

test.describe("repository summaries", () => {
  test("hides a configured non-github default host in repo labels", async ({ page }) => {
    const server = await startIsolatedE2EServerWithOptions({
      defaultPlatformHost: "ghe.example.com",
    });
    try {
      await page.goto(`${server.info.base_url}/repos`);

      const repoCards = page.locator(".repo-card");
      const enterpriseCard = repoCards
        .filter({
          has: page.getByRole("button", {
            name: /enterprise\s*\/\s*service/,
          }),
        })
        .first();
      await expect(enterpriseCard).toBeVisible();
      await expect(enterpriseCard.getByText("ghe.example.com")).toHaveCount(0);

      const githubCard = repoCards
        .filter({
          has: page.getByRole("button", {
            name: /acme\s*\/\s*widgets/,
          }),
        })
        .first();
      await expect(githubCard).toBeVisible();
      await expect(githubCard.getByText("github.com")).toBeVisible();
    } finally {
      await server.stop();
    }
  });

  test("opens the source browser from a repository card through the real backend", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const readmeLoaded = repoBrowserBlobLoaded(page, "README.md");
      await page.goto(`${server.info.base_url}/repos`);

      const widgetsCard = page
        .locator(".repo-card")
        .filter({
          has: page.getByRole("button", {
            name: /acme\s*\/\s*widgets/,
          }),
        })
        .first();
      await widgetsCard.waitFor({ state: "visible", timeout: 10_000 });
      await widgetsCard.getByRole("button", { name: "View repository source for acme widgets on github.com" }).click();
      await readmeLoaded;

      await expect(page).toHaveURL(/\/repo\/browser\?provider=github/);
      const browser = page.getByRole("region", { name: "Repository source browser" });
      await expect(browser).toBeVisible();
      await expect(browser.locator(".repo-browser__repo")).toHaveText("acme/widgets");
      const viewer = browser.getByRole("main", { name: "Selected file" });
      await expect(viewer.locator(".repo-browser__path")).toContainText("README.md");
      await expect(viewer.locator(".repo-browser__source")).toContainText("# Widget Service");
    } finally {
      await server.stop();
    }
  });

  test("remembers filters after tab changes", async ({ page }) => {
    await page.goto("/repos");

    await page.getByPlaceholder("Filter repositories").fill("acme");
    await page.getByRole("button", { name: "Has issues" }).click();
    await page.locator(".repo-page__sort-dropdown").getByRole("button", { name: "Name" }).click();
    await page.locator(".filter-dropdown").getByRole("button", { name: "Open issues" }).click();

    const repoCards = page.locator(".repo-card");
    await expect(repoCards).toHaveCount(2);
    await expect(repoCards.nth(0).getByRole("button", { name: /acme\s*\/\s*widgets/ })).toBeVisible();
    await expect(repoCards.nth(1).getByRole("button", { name: /acme\s*\/\s*tools/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /acme\s*\/\s*archived/ })).toHaveCount(0);

    await page.getByRole("button", { name: "PRs", exact: true }).click();
    await expect(page).toHaveURL(/\/pulls$/);

    await page.getByRole("button", { name: "Repos", exact: true }).click();
    await expect(page).toHaveURL(/\/repos$/);
    await expect(page.getByPlaceholder("Filter repositories")).toHaveValue("acme");
    await expect(page.getByRole("button", { name: "Has issues" })).toHaveClass(/repo-page__filter--active/);
    await expect(page.locator(".repo-page__sort-dropdown").getByRole("button", { name: "Open issues" })).toBeVisible();

    await expect(repoCards).toHaveCount(2);
    await expect(repoCards.nth(0).getByRole("button", { name: /acme\s*\/\s*widgets/ })).toBeVisible();
    await expect(repoCards.nth(1).getByRole("button", { name: /acme\s*\/\s*tools/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /acme\s*\/\s*archived/ })).toHaveCount(0);
  });

  test("shows repo stats and can create an issue", async ({ page }) => {
    await page.goto("/repos");

    const widgetsCard = page
      .locator(".repo-card")
      .filter({
        has: page.getByRole("button", {
          name: /acme\s*\/\s*widgets/,
        }),
      })
      .first();

    await widgetsCard.waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.getByText("Total repos")).toBeVisible();
    await expect(widgetsCard).toContainText("Open PRs");
    await expect(widgetsCard).toContainText("Recent open issues");
    await expect(
      widgetsCard.getByRole("link", {
        name: "Open acme/widgets on GitHub",
      }),
    ).toHaveAttribute("href", "https://github.com/acme/widgets");

    await expect(widgetsCard.getByRole("button", { name: "View PRs" })).toHaveCount(0);
    await expect(widgetsCard.getByRole("button", { name: "View issues" })).toHaveCount(0);

    await widgetsCard.getByRole("button", { name: /\d+\s+Open PRs/ }).click();
    await expect(page).toHaveURL(/\/pulls$/);

    await page.goto("/repos");
    await widgetsCard.waitFor({ state: "visible", timeout: 10_000 });
    await widgetsCard.getByRole("button", { name: /\d+\s+Open issues/ }).click();
    await expect(page).toHaveURL(/\/issues$/);

    await page.goto("/repos");
    await widgetsCard.waitFor({ state: "visible", timeout: 10_000 });
    await widgetsCard.getByRole("button", { name: "New issue" }).click();

    const dialog = page.getByRole("dialog", {
      name: "New issue in acme/widgets",
    });
    await expect(dialog).toBeVisible();
    await dialog.getByPlaceholder("Issue title").fill("Repo overview follow-up");

    const bodyEditor = dialog.getByRole("textbox", {
      name: "Describe the problem, context, or follow-up work",
    });
    await bodyEditor.click();
    await page.keyboard.type("Add additional filters @al");
    await expect(page.getByRole("option", { name: /@alice/ })).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(bodyEditor).toContainText("@alice");

    await dialog.getByRole("button", { name: "Create issue" }).click();

    await expect(page).toHaveURL(/\/issues\/github\/acme\/widgets\/\d+$/);
    await page.locator(".issue-detail").waitFor({
      state: "visible",
      timeout: 10_000,
    });
    await expect(page.locator(".issue-detail")).toContainText("Repo overview follow-up");
    await expect(
      page.locator(".issue-detail .label-pill", {
        hasText: "created-from-repos",
      }),
    ).toBeVisible();
  });
});
