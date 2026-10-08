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

let mounted: MountedBrowserApp | undefined;

beforeEach(async () => {
  await page.viewport(1280, 900);
});

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  localStorage.clear();
  sessionStorage.clear();
  await resetKeyboardModuleState();
});

it.each([
  { itemType: "pr", number: 42, title: "Add browser regression coverage" },
  { itemType: "issue", number: 7, title: "Theme toggle does not stick" },
])(
  "keeps a cached $itemType readable when returning while the backend is slow",
  async ({ itemType, number, title }) => {
    const repo = {
      provider: "github",
      platform_host: "github.com",
      platform_repo_id: 1001,
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    };
    let omitSubject = false;
    mounted = await mountBrowserApp("/?view=threaded&collapsed=1", {
      overrides: [
        ({ url }) => {
          if (url.pathname === "/api/v1/settings")
            return jsonResponse({
              ...mockSettings,
              workspaces: { ...mockSettings.workspaces, show_agent_status_in_lists: false },
            });
          if (url.pathname !== "/api/v1/activity") return null;
          return jsonResponse({
            items: [],
            item_activity: omitSubject
              ? []
              : [
                  {
                    activity_at: new Date().toISOString(),
                    item_number: number,
                    item_state: "open",
                    item_title: title,
                    item_author: "user-a",
                    item_type: itemType,
                    item_url: `https://github.com/acme/widgets/${itemType === "pr" ? "pull" : "issues"}/${number}`,
                    repo_owner: repo.owner,
                    repo_name: repo.name,
                    repo,
                  },
                ],
            capped: false,
            event_cursor: "cursor",
          });
        },
      ],
    });
    const healthyFetch = globalThis.fetch;
    let pending = Promise.withResolvers<void>();
    let holdBackend = false;
    let allowActivityRefresh = false;
    let heldReads = 0;
    let refreshed = false;
    globalThis.fetch = async (input, init) => {
      const request =
        input instanceof Request ? input : new Request(new URL(String(input), window.location.href), init);
      if (holdBackend && !(allowActivityRefresh && new URL(request.url).pathname === "/api/v1/activity")) {
        heldReads += 1;
        await pending.promise;
      }
      const response = await healthyFetch(input, init);
      const collection = itemType === "pr" ? "pulls" : "issues";
      if (
        request.method === "GET" &&
        new URL(request.url).pathname.endsWith(`/${collection}/github/acme/widgets/${number}`)
      ) {
        const detail = await response.json();
        const field = itemType === "pr" ? "merge_request" : "issue";
        return jsonResponse({
          ...detail,
          repo: { ...detail.repo, ...repo },
          [field]: { ...detail[field], Title: refreshed ? `Updated: ${title}` : title },
        });
      }
      return response;
    };
    try {
      await vi.waitFor(() => expect(document.querySelector(".item-title")?.textContent).toBe(title));
      (document.querySelector(".item-title") as HTMLElement).click();
      await vi.waitFor(() => expect(document.querySelector(".detail-title")?.textContent).toBe(title));
      const savedURL = window.location.pathname + window.location.search;
      const { navigate } = await import("./lib/stores/router.svelte.js");
      navigate("/workspaces");
      await vi.waitFor(() => expect(document.querySelector(".activity-feed")).toBeNull());

      holdBackend = true;
      navigate(savedURL);
      flushSync();
      await vi.waitFor(() => expect(heldReads).toBeGreaterThan(0));
      expect(document.querySelector(".item-title")?.textContent).toBe(title);
      expect(document.querySelector(".detail-title")?.textContent).toBe(title);
      refreshed = true;
      holdBackend = false;
      pending.resolve();
      await vi.waitFor(() => expect(document.querySelector(".detail-title")?.textContent).toBe(`Updated: ${title}`));

      // The selected detail stays open when a refresh removes its feed row.
      pending = Promise.withResolvers<void>();
      holdBackend = true;
      allowActivityRefresh = true;
      omitSubject = true;
      emitBrowserEventSource("data_changed", {});
      await vi.waitFor(() => expect(document.querySelector(".item-title")).toBeNull());
      expect(document.querySelector(".detail-title")?.textContent).toBe(`Updated: ${title}`);
    } finally {
      holdBackend = false;
      pending.resolve();
    }
  },
);
