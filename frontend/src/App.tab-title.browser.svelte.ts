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
const workspace = {
  id: "ws-title",
  repo: pull.repo,
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widgets",
  item_type: "pull_request",
  item_number: 42,
  source_item_visible: true,
  git_head_ref: "feature/auth",
  worktree_path: "/tmp/worktrees/ws-title",
  tmux_session: "",
  status: "ready",
  enrichment_status: "fresh",
  created_at: "2026-04-10T12:00:00Z",
  mr_title: "Workspace title",
};
const routes: MockRouteOverride = (req) => {
  if (req.method !== "GET") return null;
  if (req.url.pathname === "/api/v1/pulls") return jsonResponse([pull]);
  if (req.url.pathname === "/api/v1/workspaces") return jsonResponse({ workspaces: [workspace] });
  if (req.url.pathname === "/api/v1/workspaces/ws-title") return jsonResponse(workspace);
  if (req.url.pathname === "/api/v1/workspaces/ws-title/runtime")
    return jsonResponse({ launch_targets: [], sessions: [] });
  return null;
};

describe("tab title navigation", () => {
  let mounted: MountedBrowserApp | null = null;
  afterEach(async () => {
    mounted?.unmount();
    localStorage.clear();
    sessionStorage.clear();
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = false;
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

  it("names the workspace loaded by the terminal view", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/terminal/ws-title", { overrides: [routes] });
    await vi.waitFor(() => expect(document.title).toBe("Workspace title · Workspaces · Forge"), WAIT);
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

  it("names the linked item while navigating the phone workspace", async () => {
    await page.viewport(390, 844);
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = true;
    mounted = await mountBrowserApp("/m/workspaces/local/ws-title", { overrides: [routes] });
    await expect.element(page.getByRole("button", { name: "Open linked PR #42" }), { timeout: WAIT }).toBeVisible();
    await vi.waitFor(() => expect(document.title).toBe("Workspace title · Workspaces · Forge"), WAIT);
    await page.getByRole("button", { name: "Open linked PR #42" }).click();
    await expect.element(page.getByRole("tab", { name: "Conversation", exact: true }), { timeout: WAIT }).toBeVisible();
    await vi.waitFor(
      () => expect(document.title).toBe("Add browser regression coverage (acme/widgets#42) · Forge"),
      WAIT,
    );
    await page.getByRole("tab", { name: "Files", exact: true }).click();
    await vi.waitFor(() => expect(window.location.pathname).toBe("/m/workspaces/local/ws-title/item/files"), WAIT);
    expect(document.title).toBe("Add browser regression coverage (acme/widgets#42) · Forge");
    await page.getByRole("button", { name: "Back to workspace terminal" }).click();
    await expect.element(page.getByRole("button", { name: "Open linked PR #42" }), { timeout: WAIT }).toBeVisible();
    await vi.waitFor(() => expect(document.title).toBe("Workspace title · Workspaces · Forge"), WAIT);
  }, 30_000);

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
});
