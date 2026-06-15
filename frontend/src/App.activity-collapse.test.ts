// Threaded activity collapse behavior exercised through the real app shell
// with the API mocked at the fetch boundary.

import { cleanup, screen, waitFor } from "@testing-library/svelte";
import { fireEvent } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { installAppDomGlobals, mountApp, resetKeyboardModuleState, type MountedApp } from "./test/appHarness.js";
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

async function mountThreadedActivity(): Promise<MountedApp> {
  const app = await mountApp("/?view=threaded", {
    overrides: [activitySettings("threaded"), activityItems(defaultEvents)],
  });
  await waitFor(() => expect(itemRows()).toHaveLength(2));
  return app;
}

function itemRows(): Element[] {
  return Array.from(document.querySelectorAll(".item-row"));
}

function eventRows(): Element[] {
  return Array.from(document.querySelectorAll(".event-row"));
}

describe("threaded activity collapse", () => {
  vi.setConfig({ testTimeout: 20_000 });

  beforeEach(() => {
    installAppDomGlobals();
  });

  afterEach(async () => {
    cleanup();
    vi.unstubAllGlobals();
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("collapses, drills into one item, and persists across reload", async () => {
    const app = await mountThreadedActivity();
    expect(eventRows().length).toBeGreaterThan(0);

    await fireEvent.click(screen.getByRole("button", { name: "Collapse all" }));
    await waitFor(() => expect(eventRows()).toHaveLength(0));
    expect(itemRows()).toHaveLength(2);

    // Drill into a single item via its caret.
    await fireEvent.click(itemRows()[0]!.querySelector(".thread-caret")!);
    await waitFor(() => expect(eventRows().length).toBeGreaterThan(0));

    // Collapse-all wrote ?collapsed=1; a reload (remount at the current
    // URL) restores the collapsed state and clears the session-only
    // single-item override.
    expect(window.location.search).toContain("collapsed=1");
    app.unmount();

    await mountApp(window.location.pathname + window.location.search, {
      overrides: [activitySettings("threaded"), activityItems(defaultEvents)],
    });
    await waitFor(() => expect(itemRows()).toHaveLength(2));
    expect(eventRows()).toHaveLength(0);
  });

  it("collapse control works while the side detail pane is open", async () => {
    await mountThreadedActivity();

    // Open a detail by clicking the item row body (not the caret).
    await fireEvent.click(itemRows()[0]!.querySelector(".item-title")!);
    await waitFor(() => expect(document.querySelector(".activity-detail")).not.toBeNull());
    expect(document.querySelector(".activity-pane")).not.toBeNull();

    // Opening the side pane switches the feed into compact mode, where the
    // control keeps its accessible name (CSS hides only the text label —
    // that visual hiding is asserted in the browser lane, see
    // tests/e2e/activity-collapse-compact-label.spec.ts). Here we verify the
    // behavior jsdom can see: compact mode is active and the control still
    // collapses the threads.
    expect(document.querySelector(".activity-feed--compact")).not.toBeNull();
    const collapseBtn = screen.getByRole("button", { name: "Collapse all" });

    await fireEvent.click(collapseBtn);
    await waitFor(() => expect(eventRows()).toHaveLength(0));
  });

  it("expand all restores every item's events", async () => {
    await mountThreadedActivity();
    const initialCount = eventRows().length;
    expect(initialCount).toBeGreaterThan(0);

    await fireEvent.click(screen.getByRole("button", { name: "Collapse all" }));
    await waitFor(() => expect(eventRows()).toHaveLength(0));

    // The control flips to Expand all; clicking it brings every event back.
    await fireEvent.click(screen.getByRole("button", { name: "Expand all" }));
    await waitFor(() => expect(eventRows()).toHaveLength(initialCount));
  });

  // Starting from flat view mode, the test switches to Threaded through the
  // View dropdown before exercising the collapse controls.
  it("switches to threaded via the View dropdown, then collapse/expand all", async () => {
    await mountApp("/", {
      overrides: [activitySettings("flat"), activityItems(defaultEvents)],
    });
    await waitFor(() => expect(document.querySelector(".activity-table .activity-row")).not.toBeNull());

    await fireEvent.click(screen.getByRole("button", { name: "View" }));
    await fireEvent.click(screen.getByRole("button", { name: "Threaded" }));
    await waitFor(() => expect(document.querySelector(".threaded-view .item-row")).not.toBeNull());

    const initialCount = eventRows().length;
    expect(initialCount).toBeGreaterThan(0);

    await fireEvent.click(screen.getByRole("button", { name: "Collapse all" }));
    await waitFor(() => expect(eventRows()).toHaveLength(0));
    expect(document.querySelector(".threaded-view .item-row")).not.toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Expand all" }));
    await waitFor(() => expect(eventRows().length).toBeGreaterThan(0));
  });

  it("a single caret expands only its own item after collapse all", async () => {
    await mountThreadedActivity();
    const fullCount = eventRows().length;
    expect(fullCount).toBeGreaterThan(1);

    await fireEvent.click(screen.getByRole("button", { name: "Collapse all" }));
    await waitFor(() => expect(eventRows()).toHaveLength(0));

    await fireEvent.click(itemRows()[0]!.querySelector(".thread-caret")!);

    // Only the clicked item's events reappear; the rest stay collapsed.
    await waitFor(() => expect(eventRows().length).toBeGreaterThan(0));
    const partial = eventRows().length;
    expect(partial).toBeLessThan(fullCount);
  });
});
