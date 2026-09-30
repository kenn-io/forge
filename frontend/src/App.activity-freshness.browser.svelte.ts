import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { flushSync } from "svelte";
import {
  emitBrowserEventSource,
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, mockSettings } from "./test/mockApiFetch.js";

vi.setConfig({ testTimeout: 30_000 });

let mounted: MountedBrowserApp | undefined;

beforeEach(async () => {
  await page.viewport(1280, 900);
});

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
  localStorage.clear();
  sessionStorage.clear();
  await resetKeyboardModuleState();
});

it.each(["navigation", "poll", "relay"])("keeps Activity ready after workspace %s", async (refresh) => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  const now = new Date().toISOString();
  let title = "Cached activity";
  let snapshotReads = 0;
  const subject = () => ({
    activity_at: now,
    item_number: 42,
    item_state: "open",
    item_title: title,
    item_author: "alice",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/42",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      platform_repo_id: 1001,
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
  });
  mounted = await mountBrowserApp("/?view=threaded&collapsed=1", {
    overrides: [
      ({ url }) => {
        if (url.pathname === "/api/v1/settings") {
          return jsonResponse({
            ...mockSettings,
            workspaces: { ...mockSettings.workspaces, show_agent_status_in_lists: false },
            activity: { ...mockSettings.activity, view_mode: "threaded", collapse_threads: true },
          });
        }
        if (url.pathname === "/api/v1/activity") {
          if (url.searchParams.has("after")) return jsonResponse({ items: [], capped: false, event_cursor: "cursor" });
          snapshotReads += 1;
          return jsonResponse({ items: [], item_activity: [subject()], capped: false, event_cursor: "cursor" });
        }
        if (url.pathname === "/api/v1/activity/thread-events") {
          return jsonResponse({
            items: [
              {
                ...subject(),
                id: "comment:1",
                cursor: "cursor",
                activity_type: "comment",
                author: "alice",
                created_at: now,
                body_preview: "Retained comment",
              },
            ],
            capped: false,
          });
        }
        return null;
      },
    ],
  });
  await vi.waitFor(() => expect(document.querySelector(".item-title")?.textContent).toContain("Cached activity"));
  (document.querySelector(".thread-caret") as HTMLButtonElement).click();
  await vi.waitFor(() => expect(document.querySelector(".event-row")?.textContent).toContain("alice"));

  const { navigate } = await import("./lib/stores/router.svelte.js");
  navigate("/workspaces");
  await vi.waitFor(() => expect(document.querySelector(".activity-feed")).toBeNull());
  const readsBeforeRefresh = snapshotReads;
  if (refresh !== "navigation") {
    title = "Fresh activity";
    if (refresh === "poll") await vi.advanceTimersByTimeAsync(60_000);
    else emitBrowserEventSource("data_changed", {});
    await vi.waitFor(() => expect(snapshotReads).toBeGreaterThan(readsBeforeRefresh));
  }

  const readsBeforeReturn = snapshotReads;
  navigate("/?view=threaded&collapsed=1");
  flushSync();
  expect(document.querySelector(".item-title")).not.toBeNull();
  expect(document.querySelector(".event-row")?.textContent).toContain("alice");
  await vi.waitFor(() => expect(document.querySelector(".item-title")?.textContent).toContain(title));
  await vi.advanceTimersByTimeAsync(100);
  expect(snapshotReads).toBe(readsBeforeReturn);
  expect(document.querySelector(".event-row")?.textContent).toContain("alice");
});
