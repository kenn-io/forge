import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  firePopstate,
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, mockPullFixture, type MockRouteOverride } from "./test/mockApiFetch.js";
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

  it("names documents, worklists, repository paths, and Activity commits during navigation", async () => {
    await page.viewport(1280, 900);
    const list = worklistResponse(5, "Review queue");
    list.items = [];
    mounted = await mountBrowserApp("/pulls", { overrides: [routes, worklistRoutes({ lists: [list], requests: [] })] });
    await vi.waitFor(() => expect(document.title).toBe("Pulls · Forge"), WAIT);
    firePopstate("/docs?folder=notes&doc=plan.md");
    await vi.waitFor(() => expect(document.title).toBe("plan.md · notes · Docs · Forge"), WAIT);
    firePopstate("/worklists/5");
    await vi.waitFor(() => expect(document.title).toBe("Review queue · Worklists · Forge"), WAIT);
    firePopstate("/repo/browser?provider=github&repo_path=acme%2Fwidgets&ref_name=main&path=src%2Fapp.ts");
    await vi.waitFor(() => expect(document.title).toBe("src/app.ts · main · acme/widgets · Repos · Forge"), WAIT);
    firePopstate(
      "/?selected=commit%3Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&provider=github&repo_path=acme%2Fwidgets&branch=main",
    );
    await vi.waitFor(() => expect(document.title).toBe("aaaaaaaaaaaa (acme/widgets) · Forge"), WAIT);
  });

  it("keeps the import title after a worklist link fails", async () => {
    await page.viewport(1280, 900);
    const failImport: MockRouteOverride = (req) =>
      req.method === "POST" && req.url.pathname === "/api/v1/worklists/import"
        ? jsonResponse(
            { title: "Bad Request", status: 400, detail: "not a worklist link", code: "validationError" },
            400,
          )
        : null;
    mounted = await mountBrowserApp("/worklists/import#v9.abc", { overrides: [failImport] });
    await expect.element(page.getByRole("alert").getByText("not a worklist link"), { timeout: WAIT }).toBeVisible();
    expect(document.title).toBe("Import worklist · Worklists · Forge");
  });

  it("names the current repository for a saved pull selection", async () => {
    await page.viewport(1280, 900);
    const list = worklistResponse(5, "Review queue");
    mounted = await mountBrowserApp(
      "/worklists/5?selected=pr%3A42&provider=github&platform_host=github.com&repo_path=old%2Fwidgets&platform_repo_id=1001",
      {
        overrides: [worklistRoutes({ lists: [list], requests: [] })],
      },
    );
    await vi.waitFor(
      () => expect(document.querySelector(".pull-detail")?.textContent).toContain("Add browser regression coverage"),
      WAIT,
    );
    await vi.waitFor(
      () => expect(document.title).toBe("Add browser regression coverage (acme/widgets#42) · Forge"),
      WAIT,
    );
  }, 30_000);

  it("names the current repository for a saved issue selection", async () => {
    await page.viewport(1280, 900);
    const list = worklistResponse(5, "Review queue");
    const missingIssue: MockRouteOverride = (req) =>
      req.method === "GET" && req.url.pathname === "/api/v1/issues/github/acme/widgets/7"
        ? jsonResponse({ title: "Not Found", status: 404, detail: "Issue unavailable", code: "notFound" }, 404)
        : null;
    mounted = await mountBrowserApp(
      "/worklists/5?selected=issue%3A7&provider=github&platform_host=github.com&repo_path=old%2Fwidgets&platform_repo_id=1001",
      {
        overrides: [missingIssue, worklistRoutes({ lists: [list], requests: [] })],
      },
    );
    await expect.element(page.getByRole("region", { name: "Worklist item context" }), { timeout: WAIT }).toBeVisible();
    await vi.waitFor(() => expect(document.title).toBe("Issue (acme/widgets#7) · Forge"), WAIT);
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
    firePopstate(
      "/?selected=commit%3Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&provider=github&repo_path=acme%2Fwidgets&branch=main",
    );
    await vi.waitFor(() => expect(document.title).toBe("aaaaaaaaaaaa (acme/widgets) · Forge"), WAIT);
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
      await vi.waitFor(() => expect(document.title).toBe("Activity · Forge"), WAIT);
      firePopstate(
        "/?selected=commit%3Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&provider=github&repo_path=acme%2Fwidgets&branch=main",
      );
      await vi.waitFor(() => expect(document.title).toBe("Activity · Forge"), WAIT);
      if (!pending) {
        firePopstate("/?desktop=1&selected=pr%3A42&provider=github&repo_path=acme%2Fwidgets");
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
