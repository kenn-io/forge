import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "../../../test/browserAppHarness.js";
import {
  createMockApiFetch,
  jsonResponse,
  type MockApiHandle,
  type MockRouteOverride,
} from "../../../test/mockApiFetch.js";
import StatusBarTestHost from "./StatusBarTestHost.svelte";

const WAIT = 10_000;

let mounted: (MountedBrowserApp | MountedStatusBar) | null = null;

interface MountedStatusBar {
  api: MockApiHandle;
  unmount: () => void;
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

async function mountStatusBar(path: string, overrides: MockRouteOverride[]): Promise<MountedStatusBar> {
  const api = createMockApiFetch(overrides);
  const originalFetch = globalThis.fetch;
  globalThis.fetch = api.fetch;

  window.history.replaceState(null, "", path);
  const { replaceUrl } = await import("../../stores/router.svelte.js");
  replaceUrl(path);

  const target = document.createElement("div");
  document.body.appendChild(target);
  const { unmount } = render(StatusBarTestHost, { target });

  return {
    api,
    unmount: () => {
      unmount();
      target.remove();
      globalThis.fetch = originalFetch;
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
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
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
