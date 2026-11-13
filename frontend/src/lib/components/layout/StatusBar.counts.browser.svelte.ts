import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import {
  emitBrowserEventSource,
  getBrowserEventSourceCount,
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "../../../test/browserAppHarness.js";
import {
  createMockApiFetch,
  jsonResponse,
  type MockApiHandle,
  type MockRouteOverride,
} from "../../../test/mockApiFetch.js";
import StatusBarTestHost from "./StatusBarTestHost.svelte";
import { Effect } from "effect";
import { makeAppRuntime } from "../../app/runtime.js";
import { setGlobalRepo } from "../../stores/filter.svelte.js";
import type { ActivityStore } from "../../stores/activity.svelte.js";
import type { PullRequest } from "../../api/types.js";
import type { StoreInstances } from "../../types.js";

const WAIT = 10_000;

let mounted: (MountedBrowserApp | MountedStatusBar) | null = null;

interface MountedStatusBar {
  api: MockApiHandle;
  unmount: () => void | Promise<void>;
}

function repo(owner: string, name: string) {
  return {
    provider: "github",
    platform_host: "github.com",
    owner,
    name,
    repo_path: `${owner}/${name}`,
  };
}

function pr(number: number, state: "open" | "closed" | "merged", owner = "acme", name = "widgets") {
  return {
    ID: number,
    Number: number,
    Title: `PR ${number}`,
    State: state,
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    repo: repo(owner, name),
  };
}

function issue(number: number, state: "open" | "closed", owner = "acme", name = "widgets") {
  return {
    ID: number,
    Number: number,
    Title: `Issue ${number}`,
    State: state,
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    repo: repo(owner, name),
  };
}

function pullsWithClosedAndMergedRows(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/pulls") return null;
    return jsonResponse([
      pr(1, "open"),
      pr(2, "open"),
      pr(3, "closed", "acme", "closed-only"),
      pr(4, "merged", "acme", "merged-only"),
    ]);
  };
}

function issuesWithClosedRows(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/issues") return null;
    return jsonResponse([issue(1, "open"), issue(2, "closed", "acme", "closed-issues")]);
  };
}

function runningSyncStatus(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/sync/status") return null;
    return jsonResponse({
      running: true,
      progress: "github.com/acme/widgets",
      last_run_at: "2026-03-30T14:00:30Z",
      last_error: "",
    });
  };
}

function activityItem(
  id: string,
  number: number,
  activityType: "new_pr" | "new_issue" | "comment",
  itemType: "pr" | "issue",
  state: "open" | "closed" | "merged",
  owner = "acme",
  name = "widgets",
) {
  return {
    id,
    cursor: id,
    repo: repo(owner, name),
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    item_type: itemType,
    item_number: number,
    item_title: `${itemType} ${number}`,
    item_url: `https://github.com/${owner}/${name}/${itemType === "pr" ? "pull" : "issues"}/${number}`,
    item_state: state,
    activity_type: activityType,
    activity_url: "",
    author: "octo",
    author_name: "Octo",
    body_preview: "",
    branch_name: "main",
    created_at: "2026-03-30T14:00:00Z",
  };
}

function notificationActivityItem(
  id: string,
  number: number,
  itemType: "pr" | "issue",
  subjectState: "open" | "closed" | "merged" | "",
  owner = "acme",
  name = "widgets",
) {
  return {
    id,
    cursor: id,
    repo: repo(owner, name),
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    item_type: itemType,
    item_number: number,
    item_title: `${itemType} ${number}`,
    item_url: `https://github.com/${owner}/${name}/${itemType === "pr" ? "pull" : "issues"}/${number}`,
    item_state: "unread",
    subject_state: subjectState,
    activity_type: "notification",
    activity_url: "",
    author: "octo",
    author_name: "Octo",
    body_preview: "review_requested",
    branch_name: "main",
    created_at: "2026-03-30T14:00:00Z",
  };
}

function pullsWithExtraOpenRows(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/pulls") return null;
    return jsonResponse([
      pr(1, "open"),
      pr(2, "open"),
      pr(3, "open", "acme", "quiet-open"),
      pr(4, "open", "acme", "older-open"),
    ]);
  };
}

function issuesWithExtraOpenRows(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/issues") return null;
    return jsonResponse([issue(1, "open"), issue(2, "open", "acme", "quiet-issues")]);
  };
}

function activityWithNewRows(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/activity") return null;
    return jsonResponse({
      capped: false,
      items: [
        activityItem("pr-1-new", 1, "new_pr", "pr", "open"),
        activityItem("pr-1-comment", 1, "comment", "pr", "open"),
        activityItem("pr-2-new", 2, "new_pr", "pr", "open"),
        activityItem("pr-3-comment", 3, "comment", "pr", "open", "acme", "quiet-open"),
        activityItem("issue-1-new", 1, "new_issue", "issue", "open"),
        activityItem("issue-2-comment", 2, "comment", "issue", "open", "acme", "quiet-issues"),
      ],
    });
  };
}

function activityWithNotificationOnlyRows(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET" || req.url.pathname !== "/api/v1/activity") return null;
    return jsonResponse({
      capped: false,
      items: [
        notificationActivityItem("ntf-pr-open", 10, "pr", "open"),
        notificationActivityItem("ntf-issue-open", 11, "issue", "open"),
        notificationActivityItem("ntf-pr-closed", 12, "pr", "closed", "acme", "closed-notification"),
      ],
    });
  };
}

async function mountStatusBar(
  path: string,
  overrides: MockRouteOverride[],
  configureStores?: (stores: StoreInstances) => void,
): Promise<MountedStatusBar> {
  const api = createMockApiFetch(overrides);
  const originalFetch = globalThis.fetch;
  globalThis.fetch = api.fetch;

  window.history.replaceState(null, "", path);
  const { replaceUrl } = await import("../../stores/router.svelte.js");
  replaceUrl(path);

  const target = document.createElement("div");
  document.body.appendChild(target);
  const runtime = makeAppRuntime();
  const { unmount } = await render(StatusBarTestHost, {
    target,
    props: { runtime, ...(configureStores && { configureStores }) },
  });

  return {
    api,
    unmount: async () => {
      unmount();
      target.remove();
      globalThis.fetch = originalFetch;
      await Effect.runPromise(runtime.disposeEffect);
    },
  };
}

function statusItemTexts(): (string | undefined)[] {
  return Array.from(document.querySelectorAll(".kit-status-bar__section--left .status-item")).map((item) =>
    item.textContent?.trim(),
  );
}

describe("status bar counts", () => {
  vi.setConfig({ testTimeout: 30_000 });

  beforeEach(async () => {
    await page.viewport(1280, 900);
    setGlobalRepo(undefined);
  });

  afterEach(async () => {
    await mounted?.unmount();
    mounted = null;
    setGlobalRepo(undefined);
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it.each([
    { kind: "issues", state: "open", path: "/issues", expected: ["0 PRs", "50 issues", "1 repos"] },
    { kind: "pulls", state: "open", path: "/pulls", expected: ["50 PRs", "0 issues", "1 repos"] },
    { kind: "issues", state: "closed", path: "/issues", expected: ["0 PRs", "0 issues", "0 repos"] },
    { kind: "pulls", state: "closed", path: "/pulls", expected: ["0 PRs", "0 issues", "0 repos"] },
    { kind: "issues", state: "all", path: "/issues", expected: ["0 PRs", "0+ issues", "0+ repos"] },
    { kind: "pulls", state: "all", path: "/pulls", expected: ["0+ PRs", "0 issues", "0+ repos"] },
    { kind: "issues", state: "all", path: "/issues", total: 150, expected: ["0 PRs", "50 issues", "1 repos"] },
    { kind: "pulls", state: "all", path: "/pulls", total: 150, expected: ["50 PRs", "0 issues", "1 repos"] },
    { kind: "issues", state: "all", path: "/workspaces", expected: ["0 PRs", "50 issues", "1 repos"] },
    { kind: "pulls", state: "all", path: "/workspaces", expected: ["50 PRs", "0 issues", "1 repos"] },
  ])("counts $state $kind on $path as $expected", async ({ kind, state, path, total = 250, expected }) => {
    const rows = Array.from({ length: total }, (_, index) =>
      kind === "issues"
        ? issue(index + 1, index < total - 50 ? "closed" : "open")
        : pr(index + 1, index < total - 50 ? "closed" : "open"),
    );
    mounted = await mountStatusBar(
      path,
      [
        (req) => {
          if (req.method !== "GET" || !["/api/v1/issues", "/api/v1/pulls"].includes(req.url.pathname)) return null;
          const requestedState = req.url.searchParams.get("state");
          const filtered =
            req.url.pathname === `/api/v1/${kind}`
              ? rows.filter((row) => !requestedState || requestedState === "all" || row.State === requestedState)
              : [];
          const limit = Number(req.url.searchParams.get("limit")) || filtered.length;
          return jsonResponse(filtered.slice(0, limit));
        },
      ],
      (stores) => {
        if (kind === "issues") stores.issues.setIssueFilterState(state);
        else stores.pulls.setFilterState(state);
      },
    );

    await vi.waitFor(() => expect(statusItemTexts()).toEqual(expected), WAIT);
  });

  it("counts only the PRs and issues the list filters keep on their pages", async () => {
    let stores: StoreInstances | undefined;
    mounted = await mountStatusBar(
      "/pulls",
      [
        (req) =>
          req.url.pathname === "/api/v1/pulls"
            ? jsonResponse([
                { ...pr(1, "open"), head_repo_kind: "fork" },
                { ...pr(2, "open", "acme", "tools"), head_repo_kind: "same_repo" },
              ])
            : null,
        (req) =>
          req.url.pathname === "/api/v1/issues"
            ? jsonResponse([{ ...issue(1, "open"), labels: [{ name: "bug", color: "d73a4a" }] }, issue(2, "open")])
            : null,
      ],
      (configured) => {
        stores = configured;
      },
    );
    await vi.waitFor(() => expect(statusItemTexts()).toEqual(["2 PRs", "2 issues", "2 repos"]), WAIT);

    stores!.pulls.cycleAttributeFilter("fork", "not_fork");
    await vi.waitFor(() => expect(statusItemTexts()).toEqual(["1 PRs", "2 issues", "1 repos"]), WAIT);
    stores!.pulls.cycleAttributeFilter("fork", "not_fork");
    await vi.waitFor(() => expect(statusItemTexts()).toEqual(["1 PRs", "2 issues", "2 repos"]), WAIT);

    stores!.issues.cycleLabelFilter("bug");
    await vi.waitFor(() => expect(statusItemTexts()).toEqual(["1 PRs", "1 issues", "2 repos"]), WAIT);
  });

  it("labels active sync with the shared working status", async () => {
    mounted = await mountStatusBar("/repos", [runningSyncStatus()]);

    await vi.waitFor(() => {
      expect(document.querySelector('[aria-label="Syncing"]')).not.toBeNull();
      expect(document.querySelector(".kit-status-bar__section--right")?.textContent).toContain(
        "syncing (github.com/acme/widgets)",
      );
    }, WAIT);
  });

  it("shows when live updates are reconnecting", async () => {
    mounted = await mountBrowserApp("/repos");
    await vi.waitFor(() => expect(getBrowserEventSourceCount()).toBe(1), WAIT);

    emitBrowserEventSource("error", {});

    await vi.waitFor(() => {
      expect(document.querySelector(".kit-status-bar__section--right")?.textContent).toContain(
        "live updates reconnecting",
      );
    }, WAIT);
  });

  it("offers a reconnect action after live updates stop", async () => {
    mounted = await mountBrowserApp("/repos");
    await vi.waitFor(() => expect(getBrowserEventSourceCount()).toBe(1), WAIT);

    emitBrowserEventSource("sync_status", { running: "yes" });
    const reconnect = page.getByRole("button", { name: "Reconnect live updates" });
    await expect.element(reconnect).toBeVisible();
    await vi.waitFor(() => expect(getBrowserEventSourceCount()).toBe(0), WAIT);
    await reconnect.click();

    await vi.waitFor(() => expect(getBrowserEventSourceCount()).toBe(1), WAIT);
    emitBrowserEventSource("open", {});
    await expect.element(reconnect).not.toBeInTheDocument();
  });

  it("counts only open PRs when the loaded pull cache includes closed and merged rows", async () => {
    mounted = await mountBrowserApp("/repos", {
      overrides: [pullsWithClosedAndMergedRows(), issuesWithClosedRows()],
    });

    await vi.waitFor(() => {
      const paths = mounted?.api.requests.map((req) => req.url.pathname) ?? [];
      expect(paths).toContain("/api/v1/pulls");
      expect(paths).toContain("/api/v1/issues");
    }, WAIT);
    await vi.waitFor(() => expect(document.querySelector(".kit-status-bar")).not.toBeNull(), WAIT);

    await vi.waitFor(() => {
      expect(statusItemTexts()).toEqual(["2 PRs", "1 issues", "1 repos"]);
    }, WAIT);
  });

  it("marks flat counts partial after a live burst exceeds the loaded page", async () => {
    const rows = Array.from({ length: 500 }, (_, index) => ({
      ...activityItem(`comment:${index + 1}`, index + 1, "comment", "pr", "open"),
      created_at: new Date(Date.now() - index * 1000).toISOString(),
    }));
    let activity: ActivityStore | undefined;
    let polled = false;
    mounted = await mountStatusBar(
      "/?view=flat",
      [
        (req) => {
          if (req.url.pathname !== "/api/v1/activity") return null;
          const delta = req.url.searchParams.get("projection") === "events";
          if (delta) polled = true;
          const items = delta ? rows.slice(0, 300) : rows.slice(300);
          return jsonResponse({
            items,
            capped: false,
            event_cursor: items[0]?.cursor,
            ...(!delta && {
              item_activity: items.map((item) => ({ ...item, activity_at: item.created_at })),
              item_activity_capped: false,
            }),
          });
        },
      ],
      (stores) => {
        activity = stores.activity;
      },
    );
    await vi.waitFor(() => expect(statusItemTexts()).toEqual(["200 PRs", "0 issues", "1 repos"]), WAIT);
    activity!.startActivityPolling();
    await vi.waitFor(() => expect(polled).toBe(true), WAIT);
    await vi.waitFor(() => {
      expect(statusItemTexts()).toEqual(["400+ PRs", "0+ issues", "1+ repos"]);
    }, WAIT);
  });

  it("uses open activity threads for activity-page counts", async () => {
    mounted = await mountBrowserApp("/?view=threaded&range=30d", {
      overrides: [pullsWithExtraOpenRows(), issuesWithExtraOpenRows(), activityWithNewRows()],
    });

    await vi.waitFor(() => {
      const paths = mounted?.api.requests.map((req) => req.url.pathname) ?? [];
      expect(paths).toContain("/api/v1/pulls");
      expect(paths).toContain("/api/v1/issues");
      expect(paths).toContain("/api/v1/activity");
    }, WAIT);
    await vi.waitFor(() => expect(document.querySelector(".kit-status-bar")).not.toBeNull(), WAIT);

    await vi.waitFor(() => {
      expect(statusItemTexts()).toEqual(["3 PRs", "2 issues", "3 repos"]);
    }, WAIT);
  });

  it("refreshes workspace counts after provider changes without reopening the lists", async () => {
    let pulls = Array.from({ length: 35 }, (_, index) => pr(index + 1, "open"));
    let issues = [issue(1, "open"), issue(2, "open", "acme", "other")];
    mounted = await mountBrowserApp("/workspaces", {
      overrides: [
        (req) => (req.url.pathname === "/api/v1/pulls" ? jsonResponse(pulls) : null),
        (req) => (req.url.pathname === "/api/v1/issues" ? jsonResponse(issues) : null),
      ],
    });

    await vi.waitFor(() => {
      expect(statusItemTexts()).toEqual(["35 PRs", "2 issues", "2 repos"]);
      expect(getBrowserEventSourceCount()).toBe(1);
    }, WAIT);

    pulls = [pr(1, "merged"), ...pulls.slice(1)];
    issues = [issue(1, "open"), issue(2, "closed", "acme", "other")];
    emitBrowserEventSource("data_changed", {});

    await vi.waitFor(() => {
      expect(statusItemTexts()).toEqual(["34 PRs", "1 issues", "1 repos"]);
    }, WAIT);

    pulls = [...pulls, pr(36, "open", "acme", "new-repo")];
    issues = [...issues, issue(3, "open", "acme", "new-repo")];
    emitBrowserEventSource("data_changed", {});

    await vi.waitFor(() => {
      expect(statusItemTexts()).toEqual(["35 PRs", "2 issues", "2 repos"]);
    }, WAIT);
  });

  it("keeps repository-filtered counts on the PR page after provider changes", async () => {
    const allPulls: PullRequest[] = await (await createMockApiFetch().fetch("/api/v1/pulls")).json();
    setGlobalRepo("github|github.com/acme/widgets");
    mounted = await mountBrowserApp("/pulls", {
      overrides: [
        (req) =>
          req.url.pathname === "/api/v1/pulls"
            ? jsonResponse(req.url.searchParams.has("repo") ? [allPulls[0]!] : allPulls)
            : null,
        (req) => (req.url.pathname === "/api/v1/issues" ? jsonResponse([]) : null),
      ],
    });

    await vi.waitFor(() => expect(getBrowserEventSourceCount()).toBe(1), WAIT);
    const filteredReads = () =>
      mounted?.api.requests.filter((req) => req.url.pathname === "/api/v1/pulls" && req.url.searchParams.has("repo"))
        .length ?? 0;
    const before = filteredReads();
    emitBrowserEventSource("data_changed", {});

    await vi.waitFor(() => {
      expect(filteredReads()).toBeGreaterThan(before);
      expect(statusItemTexts()).toEqual(["1 PRs", "0 issues", "1 repos"]);
    }, WAIT);
    // The PR page's counts come from its filtered list; the workspace search
    // cache is not read in the background.
    expect(
      mounted?.api.requests.some((req) => req.url.pathname === "/api/v1/pulls" && !req.url.searchParams.has("repo")),
    ).toBe(false);
  });

  it("uses open activity threads for mobile activity counts", async () => {
    mounted = await mountStatusBar("/m/activity?view=threaded&range=30d", [
      pullsWithExtraOpenRows(),
      issuesWithExtraOpenRows(),
      activityWithNewRows(),
    ]);

    await vi.waitFor(() => {
      const paths = mounted?.api.requests.map((req) => req.url.pathname) ?? [];
      expect(paths).toContain("/api/v1/pulls");
      expect(paths).toContain("/api/v1/issues");
      expect(paths).toContain("/api/v1/activity");
    }, WAIT);
    await vi.waitFor(() => expect(document.querySelector(".kit-status-bar")).not.toBeNull(), WAIT);

    await vi.waitFor(() => {
      expect(statusItemTexts()).toEqual(["3 PRs", "2 issues", "3 repos"]);
    }, WAIT);
  });

  it("uses notification subject state for activity-page counts", async () => {
    mounted = await mountBrowserApp("/?view=threaded&range=30d", {
      overrides: [pullsWithExtraOpenRows(), issuesWithExtraOpenRows(), activityWithNotificationOnlyRows()],
    });

    await vi.waitFor(() => {
      const paths = mounted?.api.requests.map((req) => req.url.pathname) ?? [];
      expect(paths).toContain("/api/v1/activity");
    }, WAIT);
    await vi.waitFor(() => expect(document.querySelector(".kit-status-bar")).not.toBeNull(), WAIT);

    await vi.waitFor(() => {
      expect(statusItemTexts()).toEqual(["1 PRs", "1 issues", "1 repos"]);
    }, WAIT);
  });
});
