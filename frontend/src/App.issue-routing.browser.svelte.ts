// Browser-tier analog of App.issue-routing.test.ts. Issue detail routes must
// preserve the platform host in detail requests (direct load and popstate), and
// the detail meta row renders assignees. The app is mounted for real through the
// browser harness with fetch mocked at the network boundary so the asserted host
// is the one the app actually sent.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource. The detail-title / meta-row / seen-host
// assertions are about exact DOM shape and the captured request log, neither of
// which the page locator API (getByText/getByRole/getByTitle/getByTestId)
// exposes, so they stay as querySelector against the real DOM and api.requests
// introspection, wrapped in vi.waitFor for the genuine async render/network
// chain.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  firePopstate,
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, type MockRequest, type MockRouteOverride } from "./test/mockApiFetch.js";

const mirrorIssueDetail = {
  issue: {
    ID: 2,
    RepoID: 2,
    GitHubID: 202,
    Number: 7,
    URL: "https://ghe.example.com/acme/widgets/issues/7",
    Title: "Mirror host issue",
    Author: "marius",
    State: "open",
    Body: "",
    CommentCount: 1,
    LabelsJSON: "[]",
    CreatedAt: "2026-03-28T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    ClosedAt: null,
    Starred: false,
  },
  events: [],
  platform_host: "ghe.example.com",
  repo_owner: "acme",
  repo_name: "widgets",
  detail_loaded: true,
  detail_fetched_at: "2026-03-30T14:00:00Z",
};

const assignedIssueDetail = {
  issue: {
    ...mirrorIssueDetail.issue,
    ID: 3,
    GitHubID: 303,
    Number: 12,
    URL: "https://ghe.example.com/acme/widgets/issues/12",
    Title: "Issue with assignees",
    CommentCount: 0,
    assignees: ["alice", "bob"],
  },
  events: [],
  platform_host: "ghe.example.com",
  repo_owner: "acme",
  repo_name: "widgets",
  detail_loaded: true,
  detail_fetched_at: "2026-03-30T14:00:00Z",
};

function mirrorIssueRoutes(detail: unknown, number: number): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET") return null;
    const { pathname } = req.url;
    const legacy = new RegExp(`^/api/v1/repos/acme/widgets/issues/${number}$`);
    const hosted = `/api/v1/host/ghe.example.com/issues/github/acme/widgets/${number}`;
    if (legacy.test(pathname) || pathname === hosted) {
      return jsonResponse(detail);
    }
    return null;
  };
}

/**
 * Hosts a request carried for the mirror issue: the provider-aware
 * /host/{platform_host}/ path segment or an explicit platform_host query
 * param on the legacy repo route.
 */
function seenHosts(requests: MockRequest[], number: number): string[] {
  const hosts: string[] = [];
  for (const req of requests) {
    const { pathname } = req.url;
    if (pathname === `/api/v1/host/ghe.example.com/issues/github/acme/widgets/${number}`) {
      hosts.push("ghe.example.com");
    } else if (pathname === `/api/v1/repos/acme/widgets/issues/${number}`) {
      hosts.push(req.url.searchParams.get("platform_host") ?? "");
    }
  }
  return hosts;
}

function detailTitle(): string {
  return document.querySelector(".issue-detail .detail-title")?.textContent ?? "";
}

// Real Chromium drives the genuine async render/network chain, which is slower
// than jsdom's synchronous fixtures, so each poll gets a generous window. The
// outer testTimeout still caps the whole case.
const WAIT = 10_000;

describe("issue route platform host", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    // The container store classifies layout by #app's clientWidth; a real
    // desktop viewport keeps the desktop issue detail DOM rendering here.
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("direct issue load preserves platform_host in detail requests", async () => {
    mounted = await mountBrowserApp("/host/ghe.example.com/issues/github/acme/widgets/7", {
      overrides: [mirrorIssueRoutes(mirrorIssueDetail, 7)],
    });

    await vi.waitFor(() => expect(detailTitle()).toContain("Mirror host issue"), WAIT);
    expect(seenHosts(mounted.api.requests, 7)).toContain("ghe.example.com");
  });

  it("popstate preserves platform_host in detail requests", async () => {
    mounted = await mountBrowserApp("/issues", {
      overrides: [mirrorIssueRoutes(mirrorIssueDetail, 7)],
    });
    await expect.element(page.getByText("Theme toggle does not stick")).toBeVisible();

    firePopstate("/host/ghe.example.com/issues/github/acme/widgets/7");

    await vi.waitFor(() => expect(detailTitle()).toContain("Mirror host issue"), WAIT);
    expect(seenHosts(mounted.api.requests, 7)).toContain("ghe.example.com");
  });
});

describe("issue detail assignees", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("renders assignees in the meta row when present", async () => {
    mounted = await mountBrowserApp("/host/ghe.example.com/issues/github/acme/widgets/12", {
      overrides: [mirrorIssueRoutes(assignedIssueDetail, 12)],
    });

    await vi.waitFor(() => expect(detailTitle()).toContain("Issue with assignees"), WAIT);
    await vi.waitFor(() => {
      const assigneeList = document.querySelector(".issue-detail .meta-row [data-user-list-editor='assignees']");
      expect(assigneeList?.textContent).toContain("alice, bob");
    }, WAIT);
  });
});
