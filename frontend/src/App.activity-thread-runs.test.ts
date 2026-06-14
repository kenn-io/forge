// Same-author event runs collapse into a single summary row in threaded
// activity, rendered through the real app shell with the API mocked at the
// fetch boundary.

import { cleanup, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { installAppDomGlobals, mountApp, resetKeyboardModuleState } from "./test/appHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const REPO = {
  provider: "github",
  platform_host: "github.com",
  owner: "acme",
  name: "widgets",
  repo_path: "acme/widgets",
  capabilities: {},
};

function prEvent(args: {
  id: string;
  type: "comment" | "review" | "commit" | "force_push" | "new_pr";
  author: string;
  createdAt: string;
}): unknown {
  return {
    id: args.id,
    cursor: args.id,
    activity_type: args.type,
    author: args.author,
    body_preview: "",
    created_at: args.createdAt,
    item_number: 42,
    item_state: "open",
    item_title: "Add browser regression coverage",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/42",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: REPO,
  };
}

const settingsOverride: MockRouteOverride = (req) => {
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
      view_mode: "threaded",
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

function activityItems(items: unknown[]): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/activity") return null;
    return jsonResponse({ capped: false, items });
  };
}

async function mountThreadedActivity(items: unknown[]): Promise<void> {
  await mountApp("/?view=threaded", {
    overrides: [settingsOverride, activityItems(items)],
  });
  await waitFor(() => expect(document.querySelector(".item-row")).not.toBeNull());
}

function collapsedEventRows(): Element[] {
  return Array.from(document.querySelectorAll(".event-row.collapsed-event"));
}

function plainEventRows(): Element[] {
  return Array.from(document.querySelectorAll(".event-row:not(.collapsed-event)"));
}

describe("threaded activity run collapse", () => {
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

  it("collapses a run of three or more reviews from the same author", async () => {
    await mountThreadedActivity([
      prEvent({ id: "r5", type: "review", author: "alice", createdAt: "2026-04-27T15:00:00Z" }),
      prEvent({ id: "r4", type: "review", author: "alice", createdAt: "2026-04-27T14:00:00Z" }),
      prEvent({ id: "r3", type: "review", author: "alice", createdAt: "2026-04-27T13:00:00Z" }),
      prEvent({ id: "r2", type: "review", author: "alice", createdAt: "2026-04-27T12:00:00Z" }),
      prEvent({ id: "r1", type: "review", author: "alice", createdAt: "2026-04-27T11:00:00Z" }),
    ]);

    const collapsed = collapsedEventRows().filter((row) => row.textContent?.includes("5 reviews"));
    expect(collapsed).toHaveLength(1);
    expect(collapsed[0]!.querySelector(".evt-review")).not.toBeNull();
    expect(plainEventRows()).toHaveLength(0);
  });

  it("collapses comments and reviews into separate runs by event type", async () => {
    await mountThreadedActivity([
      prEvent({ id: "c3", type: "comment", author: "alice", createdAt: "2026-04-27T16:00:00Z" }),
      prEvent({ id: "c2", type: "comment", author: "alice", createdAt: "2026-04-27T15:00:00Z" }),
      prEvent({ id: "c1", type: "comment", author: "alice", createdAt: "2026-04-27T14:00:00Z" }),
      prEvent({ id: "r3", type: "review", author: "alice", createdAt: "2026-04-27T13:00:00Z" }),
      prEvent({ id: "r2", type: "review", author: "alice", createdAt: "2026-04-27T12:00:00Z" }),
      prEvent({ id: "r1", type: "review", author: "alice", createdAt: "2026-04-27T11:00:00Z" }),
    ]);

    expect(collapsedEventRows().filter((row) => row.textContent?.includes("3 comments"))).toHaveLength(1);
    expect(collapsedEventRows().filter((row) => row.textContent?.includes("3 reviews"))).toHaveLength(1);
  });

  it("leaves short runs of comments unrolled", async () => {
    await mountThreadedActivity([
      prEvent({ id: "c2", type: "comment", author: "alice", createdAt: "2026-04-27T13:00:00Z" }),
      prEvent({ id: "c1", type: "comment", author: "alice", createdAt: "2026-04-27T12:00:00Z" }),
    ]);

    expect(collapsedEventRows()).toHaveLength(0);
    expect(plainEventRows()).toHaveLength(2);
  });
});
