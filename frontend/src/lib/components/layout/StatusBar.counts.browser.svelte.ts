import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "../../../test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "../../../test/mockApiFetch.js";

const WAIT = 10_000;

let mounted: MountedBrowserApp | null = null;

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
    await vi.waitFor(() => expect(document.querySelector(".status-bar")).not.toBeNull(), WAIT);

    const statusItems = Array.from(document.querySelectorAll(".status-left .status-item"));
    expect(statusItems.map((item) => item.textContent?.trim())).toEqual(["2 PRs", "1 issues", "1 repos"]);
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
    await vi.waitFor(() => expect(document.querySelector(".status-bar")).not.toBeNull(), WAIT);

    const statusItems = Array.from(document.querySelectorAll(".status-left .status-item"));
    expect(statusItems.map((item) => item.textContent?.trim())).toEqual(["3 PRs", "2 issues", "3 repos"]);
  });
});
