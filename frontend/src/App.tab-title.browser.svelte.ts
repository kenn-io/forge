import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { tick } from "svelte";

import {
  firePopstate,
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, mockIssueFixture, mockPullFixture, type MockRouteOverride } from "./test/mockApiFetch.js";
import { worklistResponse, worklistRoutes } from "./test/worklistFixtures.js";

const WAIT = 10_000;

const pull = mockPullFixture(42);
const routes: MockRouteOverride = (req) =>
  req.method === "GET" && req.url.pathname === "/api/v1/pulls" ? jsonResponse([pull]) : null;
const unready: MockRouteOverride = (req) =>
  req.url.pathname === "/healthz" ? jsonResponse({ status: "starting" }, 503) : null;

describe("tab title navigation", () => {
  let mounted: MountedBrowserApp | null = null;
  afterEach(async () => {
    mounted?.unmount();
    localStorage.clear();
    sessionStorage.clear();
    await page.viewport(1280, 900);
    await resetKeyboardModuleState();
  });

  it("updates the title as a pull opens and the user visits issues", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/pulls", { overrides: [routes] });
    await vi.waitFor(() => expect(document.title).toBe("Pulls · Forge"), WAIT);
    await vi.waitFor(() => expect(document.querySelector(".pull-item")).not.toBeNull(), WAIT);
    await page.elementLocator(document.querySelector(".pull-item")!).click();
    await vi.waitFor(
      () => expect(document.title).toBe("Add browser regression coverage (acme/widgets#42) · Forge"),
      WAIT,
    );
    await page
      .getByRole("navigation", { name: "Page", exact: true })
      .getByRole("button", { name: "Issues", exact: true })
      .click();
    await vi.waitFor(() => expect(document.title).toBe("Issues · Forge"), WAIT);
  });

  it("updates the title when the keyboard selects an issue", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/issues");
    await vi.waitFor(() => expect(document.body.textContent).toContain("Theme toggle does not stick"), WAIT);
    pressKey("j");
    await vi.waitFor(() => expect(document.title).toBe("Theme toggle does not stick (acme/widgets#7) · Forge"), WAIT);
  }, 30_000);

  it("uses the current repository path for a saved worklist item", async () => {
    const list = worklistResponse(5, "Review queue");
    const item = list.items[1]!;
    item.repo = { ...item.repo, owner: "acme", name: "renamed", repo_path: "acme/renamed" };
    const issue = mockIssueFixture(7);
    const release = Promise.withResolvers<void>();
    let requested = false;
    const heldList: MockRouteOverride = (req) => {
      if (req.method !== "GET" || req.url.pathname !== "/api/v1/worklists/5") return null;
      requested = true;
      return new Response(
        new ReadableStream({
          async start(controller) {
            await release.promise;
            controller.enqueue(new TextEncoder().encode(JSON.stringify(list)));
            controller.close();
          },
        }),
        { headers: { "Content-Type": "application/json" } },
      );
    };
    const issueDetail: MockRouteOverride = (req) =>
      req.method === "GET" && req.url.pathname === "/api/v1/issues/github/acme/renamed/7"
        ? jsonResponse({
            issue,
            repo: { ...item.repo },
            events: [],
            repo_owner: "acme",
            repo_name: "renamed",
            detail_loaded: true,
          })
        : null;
    mounted = await mountBrowserApp(
      "/worklists/5?selected=issue%3A7&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&platform_repo_id=1001",
      { overrides: [issueDetail, heldList, worklistRoutes({ lists: [list], requests: [] })] },
    );
    try {
      await vi.waitFor(() => expect(requested).toBe(true), WAIT);
      expect(document.title).toBe("Issue (acme/widgets#7) · Forge");
    } finally {
      release.resolve();
    }
    await vi.waitFor(() => expect(document.title).toBe("Theme toggle does not stick (acme/renamed#7) · Forge"), WAIT);
  }, 30_000);

  it.each(["failed", "missing"] as const)(
    "uses the worklists title when a saved list is %s",
    async (state) => {
      const failDetail: MockRouteOverride = (req) =>
        state === "failed" && req.method === "GET" && req.url.pathname === "/api/v1/worklists/5"
          ? jsonResponse({ title: "Unavailable", status: 503, detail: "Worklist unavailable" }, 503)
          : null;
      mounted = await mountBrowserApp(
        "/worklists/5?selected=issue%3A7&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&platform_repo_id=1001",
        {
          overrides: [
            failDetail,
            worklistRoutes({ lists: state === "failed" ? [worklistResponse(5, "Review queue")] : [], requests: [] }),
          ],
        },
      );
      const status =
        state === "failed"
          ? page.getByRole("button", { name: "Retry" })
          : page.getByText("No worklists", { exact: true });
      await expect.element(status, { timeout: WAIT }).toBeVisible();
      await vi.waitFor(() => expect(document.title).toBe("Worklists · Forge"), WAIT);
    },
    30_000,
  );

  it("names Activity selections during startup", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/", { overrides: [unready] });
    firePopstate("/?selected=pr%3A42&provider=github&repo_path=acme%2Fwidgets");
    await vi.waitFor(() => expect(document.title).toBe("Pull request (acme/widgets#42) · Forge"), WAIT);
  });

  it("keeps the phone Activity title while startup is pending", async () => {
    await page.viewport(390, 844);
    mounted = await mountBrowserApp("/?selected=pr%3A42&provider=github&repo_path=acme%2Fwidgets", {
      overrides: [unready],
    });
    await tick();
    expect(document.title).toBe("Activity · Forge");
  }, 30_000);

  it("shows the selected pull title when desktop mode overrides phone Activity", async () => {
    await page.viewport(390, 844);
    mounted = await mountBrowserApp("/?selected=pr%3A42&provider=github&repo_path=acme%2Fwidgets");
    await expect.element(page.getByRole("button", { name: "Menu" }), { timeout: WAIT }).toBeVisible();
    await tick();
    expect(document.title).toBe("Activity · Forge");
    firePopstate("/?desktop=1&selected=pr%3A42&provider=github&repo_path=acme%2Fwidgets");
    await tick();
    await vi.waitFor(
      () => expect(document.title).toBe("Add browser regression coverage (acme/widgets#42) · Forge"),
      WAIT,
    );
  }, 30_000);
});
