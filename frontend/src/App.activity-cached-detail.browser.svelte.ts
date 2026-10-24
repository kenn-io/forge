import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { flushSync } from "svelte";
import { Effect } from "effect";
import { RepositoryReads } from "./lib/api/repository-reads.js";
import {
  emitBrowserEventSource,
  firePopstate,
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, mockSettings, mockIssueFixture, mockPullFixture } from "./test/mockApiFetch.js";

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

      const catalogReads = () => mounted!.api.requests.filter(({ url }) => url.pathname === "/api/v1/repos").length;
      const initialCatalogReads = catalogReads();
      const header = page.getByRole("navigation", { name: "Page", exact: true });
      await header.getByRole("button", { name: "Workspaces", exact: true }).click();
      await header.getByRole("button", { name: "Activity", exact: true }).click();
      await vi.waitFor(() => expect(repositoryReads).toBeGreaterThan(initialReads));
      await expect.element(merge).toBeVisible();
      await expect.element(merge).toBeDisabled();
      await expect.element(merge).toHaveAttribute("title", "Checking merge settings");
      expect(catalogReads()).toBe(initialCatalogReads);
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
  { itemType: "pr", number: 42, title: "Add browser regression coverage", removeRow: false },
  { itemType: "issue", number: 7, title: "Theme toggle does not stick", removeRow: false },
  { itemType: "pr", number: 42, title: "Add browser regression coverage", removeRow: true },
  { itemType: "issue", number: 7, title: "Theme toggle does not stick", removeRow: true },
])(
  "keeps a cached $itemType readable when returning while the backend is slow (removed row: $removeRow)",
  async ({ itemType, number, title, removeRow }) => {
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
              repos: [
                {
                  ...mockSettings.repos[0],
                  ...repo,
                  owner: "former-team",
                  name: "former-widgets",
                  repo_path: "former-team/former-widgets",
                  tracked_repo_path: "acme/widgets",
                },
              ],
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
          [field]: {
            ...detail[field],
            Title: refreshed ? `Updated: ${title}` : title,
            Body: "Cached discussion remains readable.",
          },
        });
      }
      return response;
    };
    try {
      await vi.waitFor(() => expect(document.querySelector(".item-title")?.textContent).toBe(title));
      (document.querySelector(".item-title") as HTMLElement).click();
      await vi.waitFor(() => expect(document.querySelector(".detail-title")?.textContent).toBe(title));
      const navigation = page.getByRole("navigation", { name: "Page", exact: true });
      await navigation.getByRole("button", { name: "Workspaces", exact: true }).click();
      await vi.waitFor(() => expect(document.querySelector(".activity-feed")).toBeNull());

      if (removeRow) {
        omitSubject = true;
        const readsBefore = mounted.api.requests.filter((call) => call.url.pathname === "/api/v1/activity").length;
        emitBrowserEventSource("data_changed", {});
        await vi.waitFor(() =>
          expect(
            mounted!.api.requests.filter((call) => call.url.pathname === "/api/v1/activity").length,
          ).toBeGreaterThan(readsBefore),
        );
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      }

      holdBackend = true;
      await navigation.getByRole("button", { name: "Activity", exact: true }).click();
      flushSync();
      await vi.waitFor(() => expect(heldReads).toBeGreaterThan(0));
      if (!removeRow) expect(document.querySelector(".item-title")?.textContent).toBe(title);
      expect(document.querySelector(".detail-title")?.textContent).toBe(title);
      await expect.element(page.getByText("Cached discussion remains readable.", { exact: true })).toBeVisible();
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

it.each([
  { collection: "pulls", number: 42, title: "Add browser regression coverage" },
  { collection: "issues", number: 7, title: "Theme toggle does not stick" },
])(
  "restores a renamed repo's cached $collection selection outside the visible list",
  async ({ collection, number, title }) => {
    mounted = await mountBrowserApp(`/${collection}/github/acme/widgets/${number}`, {
      overrides: [
        ({ url }) => {
          if (url.pathname === `/api/v1/${collection}`) return jsonResponse([]);
          if (collection === "issues" && /^\/api\/v1\/issues\/github\/acme\/widgets\/[78]/.test(url.pathname)) {
            const issue = {
              ...mockIssueFixture(7),
              ...(url.pathname.includes("/widgets/8") ? { Number: 8, Title: "Another issue" } : {}),
            };
            return jsonResponse({
              issue,
              repo: { ...issue.repo, platform_repo_id: 1001 },
              events: [],
              repo_owner: "acme",
              repo_name: "widgets",
              platform_host: "github.com",
              detail_loaded: true,
            });
          }
          if (url.pathname === "/api/v1/settings")
            return jsonResponse({
              ...mockSettings,
              repos: [
                {
                  ...mockSettings.repos[0],
                  provider: "github",
                  platform_host: "github.com",
                  platform_repo_id: 1001,
                  owner: "former-team",
                  name: "former-widgets",
                  repo_path: "former-team/former-widgets",
                  tracked_repo_path: "acme/widgets",
                },
              ],
            });
          return null;
        },
      ],
    });
    await vi.waitFor(() => expect(document.querySelector(".detail-title")?.textContent).toBe(title));
    firePopstate(`/${collection}/github/acme/widgets/${collection === "pulls" ? 55 : 8}`);
    await vi.waitFor(() =>
      expect(document.querySelector(".detail-title")?.textContent).toBe(
        collection === "pulls" ? "Refactor theme system" : "Another issue",
      ),
    );
    const header = page.getByRole("navigation", { name: "Page", exact: true });
    await header.getByRole("button", { name: "Workspaces", exact: true }).click();
    await vi.waitFor(() => expect(document.querySelector(".detail-title")).toBeNull());
    const pending = Promise.withResolvers<void>();
    const healthyFetch = globalThis.fetch;
    let heldReads = 0;
    globalThis.fetch = async (input, init) => {
      heldReads++;
      await pending.promise;
      return healthyFetch(input, init);
    };
    try {
      firePopstate(`/${collection}/github/acme/widgets/${number}`);
      await vi.waitFor(() => expect(heldReads).toBeGreaterThan(0));
      expect(document.querySelector(".detail-title")?.textContent).toBe(title);
    } finally {
      pending.resolve();
    }
  },
);

it.each(
  [
    { view: "Activity PR", collection: "pulls", itemType: "pr", number: 42, activity: true },
    { view: "Activity issue", collection: "issues", itemType: "issue", number: 7, activity: true },
    { view: "PR list", collection: "pulls", itemType: "pr", number: 42, activity: false },
    { view: "issue list", collection: "issues", itemType: "issue", number: 7, activity: false },
  ].flatMap((scenario) =>
    ["moved/widgets", "acme/widgets"].flatMap((trackedPath) =>
      [false, true].flatMap((filtered) =>
        (filtered && trackedPath === "acme/widgets" ? ["current", "empty", "unavailable"] : ["current"]).map(
          (catalog) => ({ ...scenario, trackedPath, filtered, catalog }),
        ),
      ),
    ),
  ),
)(
  "opens the current repository in $view when a pinned path is reused (tracked: $trackedPath, filtered: $filtered, catalog: $catalog)",
  async ({ collection, itemType, number, activity, trackedPath, filtered, catalog }) => {
    const item = collection === "pulls" ? mockPullFixture(42) : mockIssueFixture(7);
    const repo = { ...item.repo, platform_repo_id: 1001 };
    const detailTitle = "Details from the current repository";
    mounted = await mountBrowserApp(activity ? "/?view=threaded&collapsed=1" : `/${collection}`, {
      overrides: [
        ({ url }) => {
          if (url.pathname === "/api/v1/repos" && catalog === "empty") return jsonResponse([]);
          if (url.pathname === "/api/v1/repos" && catalog === "unavailable")
            return jsonResponse({ title: "Unavailable" }, 503);
          if (url.pathname === "/api/v1/settings")
            return jsonResponse({
              ...mockSettings,
              repos: [
                {
                  ...mockSettings.repos[0],
                  ...repo,
                  owner: "acme",
                  name: "widgets",
                  // This pinned repository moved away; another ID now owns its old path.
                  platform_repo_id: 2002,
                  tracked_repo_path: trackedPath,
                },
              ],
            });
          if (url.pathname === `/api/v1/${collection}`) return jsonResponse(filtered ? [] : [{ ...item, repo }]);
          if (url.pathname === "/api/v1/activity")
            return jsonResponse({
              items: [],
              item_activity: filtered
                ? []
                : [
                    {
                      activity_at: new Date().toISOString(),
                      item_number: number,
                      item_state: "open",
                      item_title: item.Title,
                      item_author: "user-a",
                      item_type: itemType,
                      repo_owner: "acme",
                      repo_name: "widgets",
                      repo,
                    },
                  ],
              capped: false,
            });
          if (url.pathname === `/api/v1/${collection}/github/acme/widgets/${number}`)
            return jsonResponse({
              [collection === "pulls" ? "merge_request" : "issue"]: {
                ...item,
                repo,
                Title: detailTitle,
                Body: "Discussion from the replacement repository.",
              },
              repo,
              repo_owner: "acme",
              repo_name: "widgets",
              platform_host: "github.com",
              detail_loaded: true,
              events: [],
            });
          return null;
        },
      ],
    });
    if (!filtered) await expect.element(page.getByText(item.Title, { exact: true }).first()).toBeVisible();
    await vi.waitFor(() =>
      expect(mounted!.api.requests.some((request) => request.url.pathname === "/api/v1/repos")).toBe(true),
    );
    // URL restoration has no stable ID, even when every matching row is filtered out.
    firePopstate(
      activity
        ? `/?view=threaded&collapsed=1&selected=${itemType}:${number}&provider=github&repo_path=acme%2Fwidgets`
        : `/${collection}/github/acme/widgets/${number}`,
    );
    await vi.waitFor(() => expect(document.querySelector(".detail-title")?.textContent).toBe(detailTitle));
    await expect.element(page.getByText("Discussion from the replacement repository.", { exact: true })).toBeVisible();
  },
);

it.each(
  [
    { view: "Activity PR", collection: "pulls", itemType: "pr", number: 42, activity: true },
    { view: "Activity issue", collection: "issues", itemType: "issue", number: 7, activity: true },
    { view: "PR list", collection: "pulls", itemType: "pr", number: 42, activity: false },
    { view: "issue list", collection: "issues", itemType: "issue", number: 7, activity: false },
  ].flatMap((scenario) =>
    ["data_changed", "config.changed", "shared refresh", "detail response"].map((update) => ({ ...scenario, update })),
  ),
)(
  "follows route reuse after mount in $view with filtered rows ($update)",
  async ({ collection, itemType, number, activity, update }) => {
    let repositoryID = 1001;
    let catalogID = 1001;
    const item = collection === "pulls" ? mockPullFixture(42) : mockIssueFixture(7);
    const route = activity
      ? `/?view=threaded&collapsed=1&selected=${itemType}:${number}&provider=github&repo_path=acme%2Fwidgets`
      : `/${collection}/github/acme/widgets/${number}`;
    mounted = await mountBrowserApp(route, {
      overrides: [
        ({ url }) => {
          if (url.pathname === "/api/v1/repos")
            return jsonResponse([
              {
                Platform: "github",
                PlatformHost: "github.com",
                PlatformRepoID: catalogID,
                Owner: "acme",
                Name: "widgets",
              },
            ]);
          if (url.pathname === `/api/v1/${collection}`) return jsonResponse([]);
          if (url.pathname === "/api/v1/activity") return jsonResponse({ items: [], item_activity: [], capped: false });
          if (url.pathname === `/api/v1/${collection}/github/acme/widgets/${number}`)
            return jsonResponse({
              [collection === "pulls" ? "merge_request" : "issue"]: {
                ...item,
                Title: repositoryID === 1001 ? "Original repository detail" : "Replacement repository detail",
                Body: repositoryID === 1001 ? "Original discussion." : "Replacement discussion.",
              },
              repo: { ...item.repo, platform_repo_id: repositoryID },
              repo_owner: "acme",
              repo_name: "widgets",
              platform_host: "github.com",
              detail_loaded: true,
              events: [],
            });
          return null;
        },
      ],
    });
    await expect.element(page.getByText("Original discussion.", { exact: true })).toBeVisible();
    // The route changes ownership while this view remains mounted and every row remains filtered out.
    repositoryID = 2002;
    if (update === "detail response") {
      // No catalog event: a foreground read must recover from the stale inferred ID.
      firePopstate(activity ? "/?view=threaded&collapsed=1" : `/${collection}`);
      flushSync();
      firePopstate(route);
    } else if (update === "shared refresh") {
      catalogID = 2002;
      await mounted.runtime.runCommand(
        Effect.gen(function* () {
          yield* (yield* RepositoryReads).refresh;
        }),
        { operation: "refresh repository fixture", safeContext: {}, onFailure: () => {} },
      ).exit;
    } else {
      catalogID = 2002;
      emitBrowserEventSource(update, { valid: true, restart_required: false });
    }
    await expect.element(page.getByText("Replacement discussion.", { exact: true })).toBeVisible();
    expect(document.querySelector(".detail-title")?.textContent).toBe("Replacement repository detail");
    await expect.element(page.getByText("Original discussion.", { exact: true })).not.toBeInTheDocument();
  },
);
