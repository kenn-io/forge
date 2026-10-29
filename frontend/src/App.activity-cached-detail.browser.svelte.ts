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

it.each([false, true])(
  "keeps merge controls visible and explains unvalidated settings (failed: %s)",
  async (failSettings) => {
    let title = "Add browser regression coverage";
    let repositoryReads = 0;
    const releaseRepositoryReads = Promise.withResolvers<void>();
    mounted = await mountBrowserApp(
      "/?view=threaded&collapsed=1&selected=pr:42&provider=github&repo_path=acme%2Fwidgets",
      {
        overrides: [
          ({ url }) => {
            if (url.pathname === "/api/v1/repo/github/acme/widgets") {
              repositoryReads += 1;
              return jsonResponse({
                Platform: "github",
                PlatformHost: "github.com",
                PlatformRepoID: 1001,
                Owner: "acme",
                Name: "widgets",
                AllowSquashMerge: true,
                AllowMergeCommit: false,
                AllowRebaseMerge: false,
                ViewerCanMerge: true,
              });
            }
            if (url.pathname !== "/api/v1/activity") return null;
            return jsonResponse({
              items: [],
              item_activity: [
                {
                  activity_at: new Date().toISOString(),
                  item_number: 42,
                  item_state: "open",
                  item_title: title,
                  item_author: "user-a",
                  item_type: "pr",
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
                },
              ],
              capped: false,
            });
          },
        ],
      },
    );
    try {
      const merge = page.getByRole("button", { name: "Squash and merge", exact: true });
      await expect.element(merge).toBeVisible();
      await document.fonts.ready;
      const button = merge.element();
      const close = page.getByRole("button", { name: "Close", exact: true }).element();
      const closeBounds = close.getBoundingClientRect().toJSON();
      const initialReads = repositoryReads;
      const healthyFetch = globalThis.fetch;
      globalThis.fetch = async (input, init) => {
        const response = await healthyFetch(input, init);
        const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
        if (url.pathname === "/api/v1/repo/github/acme/widgets") {
          await releaseRepositoryReads.promise;
          if (failSettings) return jsonResponse({ detail: "Repository settings unavailable" }, 403);
        }
        return response;
      };

      title = "Updated Activity title";
      emitBrowserEventSource("data_changed", {});
      await vi.waitFor(() => expect(document.querySelector(".item-title")?.textContent).toBe(title));
      flushSync();
      expect(button.isConnected).toBe(true);
      expect(merge.element()).toBe(button);
      expect(close.getBoundingClientRect().toJSON()).toEqual(closeBounds);
      expect(repositoryReads).toBe(initialReads);

      const header = page.getByRole("navigation", { name: "Page", exact: true });
      await header.getByRole("button", { name: "Workspaces", exact: true }).click();
      await header.getByRole("button", { name: "Activity", exact: true }).click();
      await vi.waitFor(() => expect(repositoryReads).toBeGreaterThan(initialReads));
      await expect.element(merge).toBeVisible();
      await expect.element(merge).toBeDisabled();
      await expect.element(merge).toHaveAttribute("title", "Checking merge settings");
    } finally {
      releaseRepositoryReads.resolve();
    }
    const merge = page.getByRole("button", { name: "Squash and merge", exact: true });
    if (failSettings) {
      await expect.element(merge).toHaveAttribute("title", "Could not load merge settings");
      await expect.element(merge).toBeDisabled();
    } else {
      await expect.element(merge).toBeEnabled();
    }
  },
);

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
