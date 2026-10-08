import { afterEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { flushSync } from "svelte";
import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, mockSettings } from "./test/mockApiFetch.js";

let mounted: MountedBrowserApp | undefined;

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  const { setGlobalRepo } = await import("./lib/stores/filter.svelte.js");
  setGlobalRepo(undefined);
  localStorage.clear();
  sessionStorage.clear();
  await resetKeyboardModuleState();
});

it("switches projects using retained Activity while the backend is pending", async () => {
  await page.viewport(1280, 900);
  const now = new Date().toISOString();
  const subjects = [
    {
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widgets",
      number: 42,
      title: "Widget activity",
    },
    {
      provider: "gitlab",
      platform_host: "gitlab.example.com",
      repo_path: "acme/tools",
      number: 17,
      title: "Tools activity",
    },
    {
      provider: "gitea",
      platform_host: "github.com",
      repo_path: "acme/widgets",
      number: 9,
      title: "Other provider activity",
    },
    {
      provider: "github",
      platform_host: "code.example.com",
      repo_path: "acme/widgets",
      number: 5,
      title: "Other host activity",
    },
  ].map((repo, index) => ({
    activity_at: now,
    item_number: repo.number,
    item_state: "open",
    item_title: repo.title,
    item_author: "alice",
    item_type: "pr",
    item_url: `https://${repo.platform_host}/${repo.repo_path}/pull/${repo.number}`,
    platform_host: repo.platform_host,
    repo_owner: "acme",
    repo_name: repo.repo_path.split("/").at(-1),
    repo: { ...repo, owner: "acme", name: repo.repo_path.split("/").at(-1), platform_repo_id: index + 1001 },
  }));
  let responseSubjects = subjects;
  let responseCapped = true;
  mounted = await mountBrowserApp("/?view=threaded&collapsed=1", {
    overrides: [
      ({ url }) => {
        if (url.pathname === "/api/v1/settings")
          return jsonResponse({
            ...mockSettings,
            repos: subjects.map((subject) => ({ ...mockSettings.repos[0], ...subject.repo })),
            workspaces: { ...mockSettings.workspaces, show_agent_status_in_lists: false },
            activity: { ...mockSettings.activity, view_mode: "threaded", collapse_threads: true },
          });
        if (url.pathname === "/api/v1/activity")
          return jsonResponse({
            items: [],
            item_activity: responseSubjects,
            capped: false,
            item_activity_capped: responseCapped,
          });
        if (url.pathname === "/api/v1/activity/thread-events")
          return jsonResponse({
            items: [
              {
                ...subjects[0],
                id: "comment:1",
                cursor: "comment-cursor",
                activity_type: "comment",
                author: "alice",
                created_at: now,
                body_preview: "Retained project comment",
              },
            ],
            capped: false,
          });
        return null;
      },
    ],
  });
  await vi.waitFor(() => expect(document.querySelectorAll(".item-title")).toHaveLength(4));
  (document.querySelector(".thread-caret") as HTMLButtonElement).click();
  await vi.waitFor(() => expect(document.querySelector(".event-row")?.textContent).toContain("alice"));

  const apiFetch = globalThis.fetch;
  let release = Promise.withResolvers<void>();
  let pendingReads = 0;
  globalThis.fetch = async (...args) => {
    const input = args[0];
    const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
    if (url.pathname === "/api/v1/activity") {
      pendingReads += 1;
      await release.promise;
    }
    return apiFetch(...args);
  };
  const { setGlobalRepo } = await import("./lib/stores/filter.svelte.js");
  try {
    setGlobalRepo("github|github.com/acme/widgets");
    flushSync();
    await vi.waitFor(() => expect(pendingReads).toBeGreaterThan(0));
    expect(Array.from(document.querySelectorAll(".item-title"), (row) => row.textContent)).toEqual(["Widget activity"]);
    expect(document.querySelector(".event-row")?.textContent).toContain("alice");
    responseSubjects = [{ ...subjects[0]!, item_title: "Fresh widget activity" }];
    responseCapped = false;
    release.resolve();
    await vi.waitFor(() => expect(document.querySelector(".item-title")?.textContent).toBe("Fresh widget activity"));

    release = Promise.withResolvers<void>();
    const readsBeforeTools = pendingReads;
    setGlobalRepo("gitlab|gitlab.example.com/acme/tools");
    flushSync();
    await vi.waitFor(() => expect(pendingReads).toBeGreaterThan(readsBeforeTools));
    expect(Array.from(document.querySelectorAll(".item-title"), (row) => row.textContent)).toEqual(["Tools activity"]);
    responseSubjects = [];
    release.resolve();
    await vi.waitFor(() => expect(document.querySelector(".item-title")).toBeNull());

    release = Promise.withResolvers<void>();
    const readsBeforeWidgets = pendingReads;
    setGlobalRepo("github|github.com/acme/widgets");
    flushSync();
    await vi.waitFor(() => expect(pendingReads).toBeGreaterThan(readsBeforeWidgets));
    expect(Array.from(document.querySelectorAll(".item-title"), (row) => row.textContent)).toEqual([
      "Fresh widget activity",
    ]);
    responseSubjects = [{ ...subjects[0]!, item_title: "Widget refreshed again" }];
    release.resolve();
    await vi.waitFor(() => expect(document.querySelector(".item-title")?.textContent).toBe("Widget refreshed again"));

    release = Promise.withResolvers<void>();
    const readsBeforeGlobal = pendingReads;
    setGlobalRepo(undefined);
    flushSync();
    await vi.waitFor(() => expect(pendingReads).toBeGreaterThan(readsBeforeGlobal));
    expect(Array.from(document.querySelectorAll(".item-title"), (row) => row.textContent)).toEqual([
      "Widget activity",
      "Tools activity",
      "Other provider activity",
      "Other host activity",
    ]);
    responseSubjects = [subjects[2]!];
    release.resolve();
    await vi.waitFor(() =>
      expect(Array.from(document.querySelectorAll(".item-title"), (row) => row.textContent)).toEqual([
        "Other provider activity",
      ]),
    );
  } finally {
    release.resolve();
  }
});
