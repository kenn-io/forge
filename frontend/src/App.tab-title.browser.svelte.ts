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

  it("names an issue opened in a worklist", async () => {
    await page.viewport(1280, 900);
    const issue = mockIssueFixture(7);
    const issueDetail: MockRouteOverride = (req) =>
      req.method === "GET" && req.url.pathname === "/api/v1/issues/github/acme/widgets/7"
        ? jsonResponse({
            issue,
            repo: { ...issue.repo, platform_repo_id: 1001 },
            events: [],
            repo_owner: "acme",
            repo_name: "widgets",
            detail_loaded: true,
          })
        : null;
    mounted = await mountBrowserApp(
      "/worklists/5?selected=issue%3A7&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&platform_repo_id=1001",
      { overrides: [issueDetail, worklistRoutes({ lists: [worklistResponse(5, "Review queue")], requests: [] })] },
    );
    await vi.waitFor(() => expect(document.title).toBe("Theme toggle does not stick (acme/widgets#7) · Forge"), WAIT);
  }, 30_000);

  it("keeps list titles during startup and navigation", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/pulls", { overrides: [unready] });
    await vi.waitFor(() => expect(document.title).toBe("Pulls · Forge"), WAIT);
    firePopstate("/issues");
    await vi.waitFor(() => expect(document.title).toBe("Issues · Forge"), WAIT);
  });

  it("names Activity selections during startup", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/", { overrides: [unready] });
    firePopstate("/?selected=pr%3A42&provider=github&repo_path=acme%2Fwidgets");
    await vi.waitFor(() => expect(document.title).toBe("Pull request (acme/widgets#42) · Forge"), WAIT);
  });

  it.each([true, false])(
    "keeps the phone Activity title with a selected item (pending: %s)",
    async (pending) => {
      await page.viewport(390, 844);
      mounted = await mountBrowserApp("/?selected=pr%3A42&provider=github&repo_path=acme%2Fwidgets", {
        overrides: pending ? [unready] : [],
      });
      if (!pending) await expect.element(page.getByRole("button", { name: "Menu" }), { timeout: WAIT }).toBeVisible();
      await tick();
      expect(document.title).toBe("Activity · Forge");
      if (!pending) {
        firePopstate("/?desktop=1&selected=pr%3A42&provider=github&repo_path=acme%2Fwidgets");
        await tick();
        await vi.waitFor(
          () => expect(document.title).toBe("Add browser regression coverage (acme/widgets#42) · Forge"),
          WAIT,
        );
      }
    },
    30_000,
  );

  it("names the design system while startup is pending", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/design-system", { overrides: [unready] });
    await expect.element(page.getByRole("heading", { name: "Onboarding prototypes" }), { timeout: WAIT }).toBeVisible();
    expect(document.title).toBe("Design system · Forge");
  });
});
