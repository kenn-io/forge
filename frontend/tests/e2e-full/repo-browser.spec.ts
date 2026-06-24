import { expect, test, type Locator, type Page, type Response } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer";

function blobResponse(page: Page, path: string): Promise<Response> {
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

function repoBrowserResponseMatches(response: Response, endpoint: "refs" | "tree", refName?: string): boolean {
  const url = new URL(response.url());
  return (
    response.request().method() === "GET" &&
    url.pathname === `/api/v1/repo/github/acme/widgets/browser/${endpoint}` &&
    (refName === undefined || url.searchParams.get("ref_name") === refName) &&
    response.ok()
  );
}

function repoBrowserResponse(page: Page, endpoint: "refs" | "tree", refName?: string): Promise<Response> {
  return page.waitForResponse((response) => repoBrowserResponseMatches(response, endpoint, refName));
}

function treeResponse(page: Page, refName: string): Promise<Response> {
  return repoBrowserResponse(page, "tree", refName);
}

function collectRepoBrowserResponseURLs(page: Page, endpoint: "refs" | "tree", refName?: string): string[] {
  const urls: string[] = [];
  page.on("response", (response) => {
    if (repoBrowserResponseMatches(response, endpoint, refName)) {
      urls.push(response.url());
    }
  });
  return urls;
}

async function expectNoMoreRepoBrowserResponses(
  page: Page,
  endpoint: "refs" | "tree",
  refName?: string,
): Promise<void> {
  await expect(
    page.waitForResponse((response) => repoBrowserResponseMatches(response, endpoint, refName), { timeout: 500 }),
  ).rejects.toThrow();
}

async function expectSingleRepoLoad(page: Page, refsURLs: string[], treeURLs: string[]): Promise<void> {
  expect(refsURLs).toHaveLength(1);
  expect(treeURLs).toHaveLength(1);
  await expectNoMoreRepoBrowserResponses(page, "refs");
  await expectNoMoreRepoBrowserResponses(page, "tree", "main");
  expect(refsURLs).toHaveLength(1);
  expect(treeURLs).toHaveLength(1);
}

async function expectResolvedBranchURL(page: Page): Promise<void> {
  await expect(page).toHaveURL(/\/repo\/browser\?.*ref_sha=[0-9a-f]{40}/);
  const url = new URL(page.url());
  expect(url.pathname).toBe("/repo/browser");
  expect(url.searchParams.get("provider")).toBe("github");
  expect(url.searchParams.get("platform_host")).toBe("github.com");
  expect(url.searchParams.get("repo_path")).toBe("acme/widgets");
  expect(url.searchParams.get("ref_type")).toBe("branch");
  expect(url.searchParams.get("ref_name")).toBe("main");
  expect(url.searchParams.get("path")).toBe("README.md");
  expect(url.searchParams.get("ref_sha")).toMatch(/^[0-9a-f]{40}$/);
}

async function expectHeadingScrolledIntoView(heading: Locator): Promise<void> {
  await expect(heading).toBeVisible();
  const scrollTop = await heading.evaluate((node) => {
    const markdown = node.closest(".repo-browser__markdown");
    if (!markdown) throw new Error("missing markdown scroller");
    return markdown.scrollTop;
  });
  expect(scrollTop).toBeGreaterThan(100);
}

test.describe("repository source browser", () => {
  test("does not reload repository data when the resolved branch SHA is added to the route", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const refsURLs = collectRepoBrowserResponseURLs(page, "refs");
      const treeURLs = collectRepoBrowserResponseURLs(page, "tree", "main");
      const refsLoaded = repoBrowserResponse(page, "refs");
      const treeLoaded = treeResponse(page, "main");

      await page.goto(
        `${server.info.base_url}/repo/browser?provider=github&repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main`,
      );
      await refsLoaded;
      await treeLoaded;

      const browser = page.getByRole("region", { name: "Repository source browser" });
      const viewer = browser.getByRole("main", { name: "Selected file" });
      await expect(viewer.locator(".repo-browser__path")).toContainText("README.md");
      await expectResolvedBranchURL(page);

      await expectSingleRepoLoad(page, refsURLs, treeURLs);
    } finally {
      await server.stop();
    }
  });

  test("opens a seeded repository through the real browser API", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.addInitScript(() => {
        localStorage.setItem("repo-browser-view-mode", "preview");
      });

      const treeLoaded = treeResponse(page, "main");
      const blobLoaded = blobResponse(page, "README.md");

      await page.goto(
        `${server.info.base_url}/repo/browser?provider=github&repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md`,
      );
      const initialTree = await treeLoaded;
      const initialTreeURL = new URL(initialTree.url());
      expect(initialTreeURL.searchParams.get("ref_sha")).not.toBe("main");
      const initialTreeBody = (await initialTree.json()) as { ref: { stale: boolean } };
      expect(initialTreeBody.ref.stale).toBe(false);
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
      await expect(viewer.getByRole("link", { name: "Tracker" })).toHaveAttribute(
        "href",
        "https://example.com/tracker.png",
      );
      await expect(viewer.locator('.repo-browser__markdown img[src="https://example.com/tracker.png"]')).toHaveCount(0);

      const guideBlobLoaded = blobResponse(page, "docs/guide.md");
      await viewer.getByRole("link", { name: "API reference" }).click();
      await guideBlobLoaded;
      await expect(viewer.locator(".repo-browser__path")).toContainText("docs/guide.md");
      await expect(page).toHaveURL(/path=docs%2Fguide\.md&mode=preview#api-reference$/);
      await expectHeadingScrolledIntoView(viewer.locator("#api-reference"));

      const directGuideBlobLoaded = blobResponse(page, "docs/guide.md");
      await page.goto(
        `${server.info.base_url}/repo/browser?provider=github&repo_path=acme%2Fwidgets&path=docs%2Fguide.md&mode=preview#api-reference`,
      );
      await directGuideBlobLoaded;
      await expectHeadingScrolledIntoView(viewer.locator("#api-reference"));

      await page.evaluate(() => {
        window.__middleman_navigate_to_route?.(
          "/repo/browser?provider=github&repo_path=acme%2Fwidgets&path=docs%2Fguide.md&mode=preview",
        );
      });
      await expect(page).toHaveURL(/path=docs%2Fguide\.md&mode=preview$/);
      await viewer.locator(".repo-browser__markdown").evaluate((node) => {
        node.scrollTop = 0;
      });
      await page.goBack();
      await expect(page).toHaveURL(/path=docs%2Fguide\.md&mode=preview#api-reference$/);
      await expectHeadingScrolledIntoView(viewer.locator("#api-reference"));

      const history = browser.getByRole("complementary", { name: "File history" });
      await expect(history).toContainText("Initial commit");
      await history.getByRole("button", { name: /Initial commit/ }).click();
      await expect(history.locator(".repo-browser__commit-detail")).toContainText("Initial commit");
    } finally {
      await server.stop();
    }
  });
});
