// Browser-tier analog of App.activity-collapse.test.ts. Threaded activity
// collapse behavior is exercised through the real app shell with the API mocked
// at the fetch boundary. A real Chromium page provides
// matchMedia/ResizeObserver/IntersectionObserver/canvas natively, so the jsdom
// installAppDomGlobals() shim is gone; the browser harness stubs only
// EventSource. DOM-shape assertions (.item-row/.event-row counts, .thread-caret
// / .item-title clicks, .activity-pane/.activity-feed--compact presence,
// window.location.search) stay as querySelector against the real DOM, since the
// page locator API only exposes getByText/getByRole/getByTitle/getByTestId.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

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

const defaultEvents = [
  event("a1", 42, "comment", "2026-03-30T14:00:00Z"),
  event("a2", 42, "review", "2026-03-30T13:00:00Z"),
  event("b1", 55, "comment", "2026-03-30T12:00:00Z"),
];

function itemRows(): Element[] {
  return Array.from(document.querySelectorAll(".item-row"));
}

function eventRows(): Element[] {
  return Array.from(document.querySelectorAll(".event-row"));
}

// Real Chromium drives the genuine async render/network chain, which is slower
// than jsdom's synchronous fixtures, so each poll gets a generous window. The
// outer testTimeout still caps the whole case.
const WAIT = 10_000;

describe("threaded activity collapse", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  async function mountThreadedActivity(): Promise<MountedBrowserApp> {
    const app = await mountBrowserApp("/?view=threaded", {
      overrides: [activitySettings("threaded"), activityItems(defaultEvents)],
    });
    await vi.waitFor(() => expect(itemRows()).toHaveLength(2), WAIT);
    return app;
  }

  beforeEach(async () => {
    // The container store classifies layout by #app's clientWidth (<500px is
    // "narrow"), and the activity feed renders an entirely different mobile DOM
    // in narrow mode without .threaded-view/.item-row. The jsdom harness forced
    // a 1280px desktop width via viewportWidth; the browser analog is sizing the
    // real Chromium viewport so the desktop threaded table renders here too.
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("collapses, drills into one item, and persists across reload", async () => {
    mounted = await mountThreadedActivity();
    expect(eventRows().length).toBeGreaterThan(0);

    await page.getByRole("button", { name: "Collapse all" }).click();
    await vi.waitFor(() => expect(eventRows()).toHaveLength(0), WAIT);
    expect(itemRows()).toHaveLength(2);

    // Drill into a single item via its caret.
    await page.elementLocator(itemRows()[0]!.querySelector(".thread-caret")!).click();
    await vi.waitFor(() => expect(eventRows().length).toBeGreaterThan(0), WAIT);

    // Collapse-all wrote ?collapsed=1; a reload (remount at the current
    // URL) restores the collapsed state and clears the session-only
    // single-item override.
    expect(window.location.search).toContain("collapsed=1");
    mounted.unmount();

    mounted = await mountBrowserApp(window.location.pathname + window.location.search, {
      overrides: [activitySettings("threaded"), activityItems(defaultEvents)],
    });
    await vi.waitFor(() => expect(itemRows()).toHaveLength(2), WAIT);
    expect(eventRows()).toHaveLength(0);
  });

  it("collapse control works while the side detail pane is open", async () => {
    mounted = await mountThreadedActivity();

    // Open a detail by clicking the item row body (not the caret).
    await page.elementLocator(itemRows()[0]!.querySelector(".item-title")!).click();
    await vi.waitFor(() => expect(document.querySelector(".activity-detail")).not.toBeNull(), WAIT);
    expect(document.querySelector(".activity-pane")).not.toBeNull();

    // Opening the side pane switches the feed into compact mode, where the
    // control keeps its accessible name (CSS hides only the text label —
    // that visual hiding is asserted in the browser lane, see
    // tests/e2e/activity-collapse-compact-label.spec.ts). Here we verify the
    // behavior compact mode is active and the control still collapses the
    // threads.
    expect(document.querySelector(".activity-feed--compact")).not.toBeNull();

    await page.getByRole("button", { name: "Collapse all" }).click();
    await vi.waitFor(() => expect(eventRows()).toHaveLength(0), WAIT);
  });

  it("expand all restores every item's events", async () => {
    mounted = await mountThreadedActivity();
    const initialCount = eventRows().length;
    expect(initialCount).toBeGreaterThan(0);

    await page.getByRole("button", { name: "Collapse all" }).click();
    await vi.waitFor(() => expect(eventRows()).toHaveLength(0), WAIT);

    // The control flips to Expand all; clicking it brings every event back.
    await page.getByRole("button", { name: "Expand all" }).click();
    await vi.waitFor(() => expect(eventRows()).toHaveLength(initialCount), WAIT);
  });

  // Starting from flat view mode, the test switches to Threaded through the
  // View dropdown before exercising the collapse controls.
  it("switches to threaded via the View dropdown, then collapse/expand all", async () => {
    mounted = await mountBrowserApp("/", {
      overrides: [activitySettings("flat"), activityItems(defaultEvents)],
    });
    await vi.waitFor(() => expect(document.querySelector(".activity-table .activity-row")).not.toBeNull(), WAIT);

    // Two buttons carry "View" in their accessible name (the Reviews view-tab
    // and the filter button labelled exactly "View"); target the filter button.
    await page.getByRole("button", { name: "View", exact: true }).click();
    await page.getByRole("button", { name: "Threaded" }).click();
    await vi.waitFor(() => expect(document.querySelector(".threaded-view .item-row")).not.toBeNull(), WAIT);

    const initialCount = eventRows().length;
    expect(initialCount).toBeGreaterThan(0);

    await page.getByRole("button", { name: "Collapse all" }).click();
    await vi.waitFor(() => expect(eventRows()).toHaveLength(0), WAIT);
    expect(document.querySelector(".threaded-view .item-row")).not.toBeNull();

    await page.getByRole("button", { name: "Expand all" }).click();
    await vi.waitFor(() => expect(eventRows().length).toBeGreaterThan(0), WAIT);
  });

  it("a single caret expands only its own item after collapse all", async () => {
    mounted = await mountThreadedActivity();
    const fullCount = eventRows().length;
    expect(fullCount).toBeGreaterThan(1);

    await page.getByRole("button", { name: "Collapse all" }).click();
    await vi.waitFor(() => expect(eventRows()).toHaveLength(0), WAIT);

    await page.elementLocator(itemRows()[0]!.querySelector(".thread-caret")!).click();

    // Only the clicked item's events reappear; the rest stay collapsed.
    await vi.waitFor(() => expect(eventRows().length).toBeGreaterThan(0), WAIT);
    const partial = eventRows().length;
    expect(partial).toBeLessThan(fullCount);
  });
});
