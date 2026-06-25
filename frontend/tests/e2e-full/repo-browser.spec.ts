import { expect, test, type Locator, type Page, type Response } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer";

type RepoBrowserEndpoint = "refs" | "tree" | "blob";

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

function repoBrowserResponseMatches(
  response: Response,
  endpoint: RepoBrowserEndpoint,
  refName?: string,
  path?: string,
): boolean {
  const url = new URL(response.url());
  return (
    response.request().method() === "GET" &&
    url.pathname === `/api/v1/repo/github/acme/widgets/browser/${endpoint}` &&
    (refName === undefined || url.searchParams.get("ref_name") === refName) &&
    (path === undefined || url.searchParams.get("path") === path) &&
    response.ok()
  );
}

function repoBrowserResponse(page: Page, endpoint: RepoBrowserEndpoint, refName?: string): Promise<Response> {
  return page.waitForResponse((response) => repoBrowserResponseMatches(response, endpoint, refName));
}

function treeResponse(page: Page, refName: string): Promise<Response> {
  return repoBrowserResponse(page, "tree", refName);
}

function collectRepoBrowserResponseURLs(
  page: Page,
  endpoint: RepoBrowserEndpoint,
  refName?: string,
  path?: string,
): string[] {
  const urls: string[] = [];
  page.on("response", (response) => {
    if (repoBrowserResponseMatches(response, endpoint, refName, path)) {
      urls.push(response.url());
    }
  });
  return urls;
}

async function expectNoMoreRepoBrowserResponses(
  page: Page,
  endpoint: RepoBrowserEndpoint,
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
  test("does not reload repository data when the no-ref route is replaced with the default ref", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const refsURLs = collectRepoBrowserResponseURLs(page, "refs");
      const treeURLs = collectRepoBrowserResponseURLs(page, "tree", "main");
      const refsLoaded = repoBrowserResponse(page, "refs");
      const treeLoaded = treeResponse(page, "main");

      await page.goto(`${server.info.base_url}/repo/browser?provider=github&repo_path=acme%2Fwidgets`);
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

  test("does not reload repository data after a user ref change updates the route", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const refsURLs = collectRepoBrowserResponseURLs(page, "refs");
      const mainTreeURLs = collectRepoBrowserResponseURLs(page, "tree", "main");
      const featureTreeURLs = collectRepoBrowserResponseURLs(page, "tree", "feature/caching");
      const refsLoaded = repoBrowserResponse(page, "refs");
      const mainTreeLoaded = treeResponse(page, "main");

      await page.goto(
        `${server.info.base_url}/repo/browser?provider=github&repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md`,
      );
      await refsLoaded;
      await mainTreeLoaded;

      const browser = page.getByRole("region", { name: "Repository source browser" });
      await browser.getByRole("button", { name: /Select repository ref: branch: main/ }).click();
      await browser.getByRole("combobox", { name: "Search repository refs" }).fill("feature");
      await expect(browser.getByRole("tab", { name: /Branches/ })).toHaveAttribute("aria-selected", "true");
      await expect(browser.getByRole("option", { name: /branch: feature\/caching/ })).toBeVisible();
      await expect(browser.getByRole("option", { name: /tag:/ })).toHaveCount(0);

      const featureTreeLoaded = treeResponse(page, "feature/caching");
      await browser.getByRole("option", { name: /branch: feature\/caching/ }).click();
      await featureTreeLoaded;
      await expect(page).toHaveURL(/ref_name=feature%2Fcaching/);

      expect(refsURLs).toHaveLength(1);
      expect(mainTreeURLs).toHaveLength(1);
      expect(featureTreeURLs).toHaveLength(1);
      await expectNoMoreRepoBrowserResponses(page, "refs");
      await expectNoMoreRepoBrowserResponses(page, "tree", "feature/caching");
      expect(refsURLs).toHaveLength(1);
      expect(mainTreeURLs).toHaveLength(1);
      expect(featureTreeURLs).toHaveLength(1);
    } finally {
      await server.stop();
    }
  });

  test("does not reload repository data when the active ref is selected again", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const refsURLs = collectRepoBrowserResponseURLs(page, "refs");
      const treeURLs = collectRepoBrowserResponseURLs(page, "tree", "main");
      const blobURLs = collectRepoBrowserResponseURLs(page, "blob", undefined, "README.md");
      const refsLoaded = repoBrowserResponse(page, "refs");
      const treeLoaded = treeResponse(page, "main");
      const blobLoaded = blobResponse(page, "README.md");

      await page.goto(
        `${server.info.base_url}/repo/browser?provider=github&repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md`,
      );
      await refsLoaded;
      await treeLoaded;
      await blobLoaded;

      const browser = page.getByRole("region", { name: "Repository source browser" });
      await expect(browser.getByRole("main", { name: "Selected file" }).locator(".repo-browser__path")).toContainText(
        "README.md",
      );
      const routeBeforeReselect = page.url();

      await browser.getByRole("button", { name: /Select repository ref: branch: main/ }).click();
      await browser.getByRole("option", { name: /branch: main/ }).click();

      await expect(browser.getByRole("combobox", { name: "Search repository refs" })).toHaveCount(0);
      expect(page.url()).toBe(routeBeforeReselect);
      expect(refsURLs).toHaveLength(1);
      expect(treeURLs).toHaveLength(1);
      expect(blobURLs).toHaveLength(1);
      await expectNoMoreRepoBrowserResponses(page, "refs");
      await expectNoMoreRepoBrowserResponses(page, "tree", "main");
      await expectNoMoreRepoBrowserResponses(page, "blob");
      expect(refsURLs).toHaveLength(1);
      expect(treeURLs).toHaveLength(1);
      expect(blobURLs).toHaveLength(1);
    } finally {
      await server.stop();
    }
  });

  test("reloads repository data when a mounted route moves the same branch to a different SHA", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const staleSHA = "0".repeat(40);
      const treeURLs = collectRepoBrowserResponseURLs(page, "tree", "main");
      const blobURLs = collectRepoBrowserResponseURLs(page, "blob", undefined, "README.md");
      const initialTreeLoaded = treeResponse(page, "main");
      const initialBlobLoaded = blobResponse(page, "README.md");

      await page.goto(
        `${server.info.base_url}/repo/browser?provider=github&repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&path=README.md`,
      );
      const initialTree = await initialTreeLoaded;
      const initialTreeBody = (await initialTree.json()) as { ref: { sha: string } };
      await initialBlobLoaded;

      const movedTreeLoaded = treeResponse(page, "main");
      const movedBlobLoaded = blobResponse(page, "README.md");
      await page.evaluate((route) => {
        window.__middleman_navigate_to_route?.(route);
      }, `/repo/browser?provider=github&repo_path=acme%2Fwidgets&ref_type=branch&ref_name=main&ref_sha=${staleSHA}&path=README.md`);
      const movedTree = await movedTreeLoaded;
      const movedTreeBody = (await movedTree.json()) as {
        ref: { sha: string; requested_sha?: string; stale: boolean };
      };
      await movedBlobLoaded;

      expect(treeURLs).toHaveLength(2);
      expect(blobURLs).toHaveLength(2);
      expect(new URL(treeURLs[1]!).searchParams.get("ref_sha")).toBe(staleSHA);
      expect(movedTreeBody.ref.sha).toBe(initialTreeBody.ref.sha);
      expect(movedTreeBody.ref.requested_sha).toBe(staleSHA);
      expect(movedTreeBody.ref.stale).toBe(true);
    } finally {
      await server.stop();
    }
  });

  test("opens the focused route repo from the command palette when a stale PR selection exists", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/pulls/github/acme/tools/1`);
      await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });

      await page.evaluate((route) => {
        window.__middleman_navigate_to_route?.(route);
      }, "/focus/pulls/github/acme/widgets/1");
      await page.locator(".focus-layout .pull-detail").waitFor({ state: "visible", timeout: 10_000 });

      const readmeLoaded = blobResponse(page, "README.md");
      await page.keyboard.press(process.platform === "darwin" ? "Meta+K" : "Control+K");
      const palette = page.getByRole("dialog", { name: "Command palette" });
      await expect(palette).toBeVisible();
      await palette.locator(".palette-input").fill("repo.browser.open");
      await palette.getByRole("button", { name: /View repository source/ }).click();
      await readmeLoaded;

      const route = new URL(page.url());
      expect(route.pathname).toBe("/repo/browser");
      expect(route.searchParams.get("repo_path")).toBe("acme/widgets");
      const browser = page.getByRole("region", { name: "Repository source browser" });
      await expect(browser.locator(".repo-browser__repo")).toHaveText("acme/widgets");
      await expect(browser.getByRole("main", { name: "Selected file" }).locator(".repo-browser__source")).toContainText(
        "# Widget Service",
      );
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
