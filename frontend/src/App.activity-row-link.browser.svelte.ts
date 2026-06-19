// Browser-tier reimplementation of frontend/tests/e2e-full/activity-row-link.spec.ts.
// Flat and threaded activity rows expose an "Open activity" link button that
// jumps straight to the provider URL via window.open without opening the in-app
// drawer, threaded rows stay clickable after the split detail pane opens, and the
// compact threaded row keeps the expand chevron visually separate from the type
// chip. All of this is real rendering / real geometry / real window.open, so it
// belongs in the browser tier. The app is mounted for real with the activity
// feed mocked at the fetch boundary.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource. window.open is spied with vi.spyOn (not mocked
// through the API layer), and the chevron/type-chip gap uses real
// getBoundingClientRect geometry.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;

function event(id: string, number: number, type: string, created: string): unknown {
  return {
    id,
    cursor: id,
    activity_type: type,
    author: "marius",
    body_preview: "",
    created_at: created,
    item_number: number,
    item_state: "open",
    item_title: number === 42 ? "Add browser regression coverage" : "Refactor theme system",
    item_type: "pr",
    item_url: `https://github.com/acme/widgets/pull/${number}`,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
      capabilities: {},
    },
  };
}

const defaultEvents = [
  event("a1", 42, "comment", "2026-03-30T14:00:00Z"),
  event("a2", 42, "review", "2026-03-30T13:00:00Z"),
  event("b1", 55, "comment", "2026-03-30T12:00:00Z"),
];

function activitySettings(viewMode: "flat" | "threaded"): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/settings") return null;
    return jsonResponse({
      repos: [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "widgets",
          repo_path: "acme/widgets",
          is_glob: false,
          matched_repo_count: 1,
        },
      ],
      activity: {
        view_mode: viewMode,
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
      },
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        renderer: "xterm",
      },
      agents: [],
    });
  };
}

function activityItems(items: unknown[]): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/activity") return null;
    return jsonResponse({ capped: false, items });
  };
}

function flatOverrides(): MockRouteOverride[] {
  return [activitySettings("flat"), activityItems(defaultEvents)];
}

function threadedOverrides(): MockRouteOverride[] {
  return [activitySettings("threaded"), activityItems(defaultEvents)];
}

function threadedItemRows(): Element[] {
  return Array.from(document.querySelectorAll(".threaded-view .item-row:not(.branch-activity-row)"));
}

function findInActivityFeed(selector: string, text: string): Element {
  return Array.from(document.querySelectorAll(`.activity-feed ${selector}`)).find((el) =>
    (el.textContent ?? "").includes(text),
  )!;
}

async function switchToThreaded(): Promise<void> {
  // Once the split detail pane is open there are two "View" filter buttons (feed
  // and detail); scope to the activity feed's button and dropdown.
  await page.elementLocator(findInActivityFeed(".filter-btn", "View")).click();
  await vi.waitFor(() => expect(document.querySelector(".activity-feed .filter-dropdown")).not.toBeNull(), WAIT);
  await page.elementLocator(findInActivityFeed(".filter-dropdown .filter-item", "Threaded")).click();
  await vi.waitFor(() => expect(document.querySelector(".threaded-view .item-row")).not.toBeNull(), WAIT);
}

describe("activity row link button", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    vi.restoreAllMocks();
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("flat row link button opens the activity URL without triggering row select", async () => {
    mounted = await mountBrowserApp("/", { overrides: flatOverrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-table .activity-row")).not.toBeNull(), WAIT);

    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    const linkBtn = document.querySelector(".activity-table .activity-row .link-btn");
    expect(linkBtn).not.toBeNull();
    await page.elementLocator(linkBtn!).click();

    await vi.waitFor(() => expect(openSpy).toHaveBeenCalled(), WAIT);
    expect(String(openSpy.mock.calls[0]![0])).toContain("github.com/acme/");
    expect(document.querySelectorAll(".activity-drawer").length).toBe(0);
  });

  it("threaded item row link button opens the item URL without expanding the item", async () => {
    mounted = await mountBrowserApp("/?view=threaded", { overrides: threadedOverrides() });
    await vi.waitFor(() => expect(threadedItemRows().length).toBeGreaterThan(0), WAIT);

    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    const linkBtn = threadedItemRows()[0]!.querySelector(".link-btn");
    expect(linkBtn).not.toBeNull();
    await page.elementLocator(linkBtn!).click();

    await vi.waitFor(() => expect(openSpy).toHaveBeenCalled(), WAIT);
    expect(String(openSpy.mock.calls[0]![0])).toMatch(/github\.com\/acme\/[^/]+\/(pull|issues)\/\d+/);
    expect(document.querySelectorAll(".activity-drawer").length).toBe(0);
  });

  it("threaded rows remain clickable after the split detail pane opens", async () => {
    await page.viewport(900, 720);
    localStorage.setItem("middleman-activity-pane-width", "1200");
    mounted = await mountBrowserApp("/", { overrides: flatOverrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-table .activity-row")).not.toBeNull(), WAIT);

    await page.elementLocator(document.querySelector(".activity-table .activity-row")!).click();
    await vi.waitFor(() => expect(document.querySelector(".activity-detail-header")).not.toBeNull(), WAIT);
    const firstSelection = document.querySelector(".activity-detail-header")?.textContent ?? "";
    expect(firstSelection).not.toBe("");

    await switchToThreaded();
    await vi.waitFor(() => expect(threadedItemRows().length).toBeGreaterThan(1), WAIT);

    await page.elementLocator(threadedItemRows()[1]!).click();
    await vi.waitFor(() => {
      expect(document.querySelector(".activity-detail-header")?.textContent ?? "").not.toBe(firstSelection);
    }, WAIT);
  });

  it("compact threaded rows keep the expand chevron separate from the type chip", async () => {
    await page.viewport(900, 720);
    mounted = await mountBrowserApp("/", { overrides: flatOverrides() });
    await vi.waitFor(() => expect(document.querySelector(".activity-table .activity-row")).not.toBeNull(), WAIT);

    await page.elementLocator(document.querySelector(".activity-table .activity-row")!).click();
    await switchToThreaded();

    const itemRow = threadedItemRows()[0]!;
    const caret = itemRow.querySelector(".thread-caret");
    const typeCell = itemRow.querySelector(".cell--type");
    expect(caret).not.toBeNull();
    expect(typeCell).not.toBeNull();

    const caretBox = caret!.getBoundingClientRect();
    const typeBox = typeCell!.getBoundingClientRect();
    expect(typeBox.x - (caretBox.x + caretBox.width)).toBeGreaterThanOrEqual(8);
  });
});
