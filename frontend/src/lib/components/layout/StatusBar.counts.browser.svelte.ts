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
    mounted = await mountBrowserApp("/?view=threaded&range=30d", {
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
});
