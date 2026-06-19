// Browser-tier reimplementation of frontend/tests/e2e-full/collapsible-repos.spec.ts.
// Repo and status group headers collapse/expand their items, keep a count while
// collapsed, persist the collapse set in localStorage, and keep that set
// independent across the pulls and issues surfaces. The app is mounted for real
// through the browser harness with the pull/issue lists mocked at the fetch
// boundary, so the rendered groups, counts, and post-reload state are genuine.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource. aria-expanded, item counts, and group counts
// are exact-DOM assertions the page locator API does not expose, so they stay as
// querySelector against the real DOM, wrapped in vi.waitFor for the async render.
//
// The keyboard-activation case (Enter/Space on the header button) stays in
// Playwright: native button activation is a real key event the browser tier's
// synthetic dispatch cannot reproduce.
//
// Seed parity (cmd/e2e-server, internal/testutil/fixtures.go): 8 open PRs
// (widgets #1/#2/#6/#7, tools #1/#10/#11/#12) and 5 open issues (widgets
// #10/#11/#13, tools #5, gitlab group/project #11).

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;

function repoRef(provider: string, host: string, owner: string, name: string) {
  return {
    provider,
    platform_host: host,
    repo_path: `${owner}/${name}`,
    owner,
    name,
    capabilities: { read_repositories: true, read_merge_requests: true, read_issues: true, read_comments: true },
  };
}

function pull(owner: string, name: string, number: number, host = "github.com", provider = "github") {
  return {
    ID: number * 100 + name.length,
    RepoID: name === "tools" ? 2 : 1,
    GitHubID: 5000 + number,
    Number: number,
    URL: `https://${host}/${owner}/${name}/pull/${number}`,
    Title: `${name} PR ${number}`,
    Author: "alice",
    State: "open",
    IsDraft: false,
    Body: "",
    HeadBranch: "feature/x",
    BaseBranch: "main",
    Additions: 1,
    Deletions: 0,
    CommentCount: 0,
    ReviewDecision: "",
    CIStatus: "success",
    CIChecksJSON: "[]",
    CreatedAt: "2026-03-29T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    MergedAt: null,
    ClosedAt: null,
    KanbanStatus: "new",
    Starred: false,
    repo_owner: owner,
    repo_name: name,
    platform_host: host,
    platform_head_sha: `${number}aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa${number}`,
    repo: repoRef(provider, host, owner, name),
    worktree_links: [],
  };
}

function issue(owner: string, name: string, number: number, host = "github.com", provider = "github") {
  return {
    ID: number * 100 + name.length + 1,
    RepoID: name === "tools" ? 2 : name === "project" ? 3 : 1,
    GitHubID: 6000 + number,
    Number: number,
    URL: `https://${host}/${owner}/${name}/issues/${number}`,
    Title: `${name} issue ${number}`,
    Author: "alice",
    State: "open",
    Body: "",
    CommentCount: 0,
    LabelsJSON: "[]",
    CreatedAt: "2026-03-28T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    ClosedAt: null,
    Starred: false,
    platform_host: host,
    repo_owner: owner,
    repo_name: name,
    repo: repoRef(provider, host, owner, name),
  };
}

const pulls = [
  pull("acme", "widgets", 1),
  pull("acme", "widgets", 2),
  pull("acme", "widgets", 6),
  pull("acme", "widgets", 7),
  pull("acme", "tools", 1),
  pull("acme", "tools", 10),
  pull("acme", "tools", 11),
  pull("acme", "tools", 12),
];

const issues = [
  issue("acme", "widgets", 10),
  issue("acme", "widgets", 11),
  issue("acme", "widgets", 13),
  issue("acme", "tools", 5),
  issue("group", "project", 11, "gitlab.com", "gitlab"),
];

function listOverrides(): MockRouteOverride[] {
  return [
    (req) => (req.method === "GET" && req.url.pathname === "/api/v1/pulls" ? jsonResponse(pulls) : null),
    (req) => (req.method === "GET" && req.url.pathname === "/api/v1/issues" ? jsonResponse(issues) : null),
  ];
}

function header(label: string): HTMLElement | null {
  return (
    (Array.from(document.querySelectorAll(".repo-header")).find((h) => (h.textContent ?? "").includes(label)) as
      | HTMLElement
      | undefined) ?? null
  );
}

function expanded(label: string): string | null {
  return header(label)?.getAttribute("aria-expanded") ?? null;
}

function groupCount(label: string): string {
  return header(label)?.querySelector(".repo-header__count")?.textContent?.trim() ?? "";
}

function count(selector: string): number {
  return document.querySelectorAll(selector).length;
}

async function clickHeader(label: string): Promise<void> {
  await page.elementLocator(header(label)!).click();
}

describe("collapsible repo groups", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
    localStorage.clear();
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("default expanded shows every PR and both headers", async () => {
    mounted = await mountBrowserApp("/pulls", { overrides: listOverrides() });

    await vi.waitFor(() => expect(count(".pull-item")).toBe(8), WAIT);
    expect(expanded("acme/widgets")).toBe("true");
    expect(expanded("acme/tools")).toBe("true");
  });

  it("collapsing acme/widgets hides its items but keeps the header and count", async () => {
    mounted = await mountBrowserApp("/pulls", { overrides: listOverrides() });
    await vi.waitFor(() => expect(count(".pull-item")).toBe(8), WAIT);

    await clickHeader("acme/widgets");

    await vi.waitFor(() => expect(count(".pull-item")).toBe(4), WAIT);
    expect(expanded("acme/widgets")).toBe("false");
    expect(groupCount("acme/widgets")).toBe("4");
    expect(expanded("acme/tools")).toBe("true");
  });

  it("expanding acme/widgets again restores its items", async () => {
    mounted = await mountBrowserApp("/pulls", { overrides: listOverrides() });
    await vi.waitFor(() => expect(count(".pull-item")).toBe(8), WAIT);

    await clickHeader("acme/widgets");
    await vi.waitFor(() => expect(count(".pull-item")).toBe(4), WAIT);
    await clickHeader("acme/widgets");

    await vi.waitFor(() => expect(count(".pull-item")).toBe(8), WAIT);
    expect(expanded("acme/widgets")).toBe("true");
  });

  it("status groups collapse like repo groups", async () => {
    localStorage.setItem("middleman:groupingMode", "byWorkflow");
    mounted = await mountBrowserApp("/pulls", { overrides: listOverrides() });

    await vi.waitFor(() => expect(expanded("New")).toBe("true"), WAIT);
    await clickHeader("New");

    await vi.waitFor(() => expect(count(".pull-item")).toBe(0), WAIT);
    expect(expanded("New")).toBe("false");
    expect(groupCount("New")).toBe("8");

    await clickHeader("New");
    await vi.waitFor(() => expect(count(".pull-item")).toBe(8), WAIT);
    expect(expanded("New")).toBe("true");
  });

  it("collapse state persists across reload", async () => {
    mounted = await mountBrowserApp("/pulls", { overrides: listOverrides() });
    await vi.waitFor(() => expect(count(".pull-item")).toBe(8), WAIT);
    await clickHeader("acme/widgets");
    await vi.waitFor(() => expect(expanded("acme/widgets")).toBe("false"), WAIT);

    // Remount in the same page (localStorage retained) to mirror a reload.
    mounted.unmount();
    mounted = await mountBrowserApp("/pulls", { overrides: listOverrides() });

    await vi.waitFor(() => expect(expanded("acme/widgets")).toBe("false"), WAIT);
    expect(count(".pull-item")).toBe(4);
  });

  it("collapse is independent across the pulls and issues surfaces", async () => {
    mounted = await mountBrowserApp("/pulls", { overrides: listOverrides() });
    await vi.waitFor(() => expect(count(".pull-item")).toBe(8), WAIT);
    await clickHeader("acme/widgets");
    await vi.waitFor(() => expect(expanded("acme/widgets")).toBe("false"), WAIT);

    mounted.unmount();
    mounted = await mountBrowserApp("/issues", { overrides: listOverrides() });

    await vi.waitFor(() => expect(count(".issue-item")).toBe(5), WAIT);
    expect(expanded("acme/widgets")).toBe("true");
  });

  it("issue list collapse, expand, and persist for acme/widgets", async () => {
    mounted = await mountBrowserApp("/issues", { overrides: listOverrides() });
    await vi.waitFor(() => expect(count(".issue-item")).toBe(5), WAIT);

    await clickHeader("acme/widgets");
    await vi.waitFor(() => expect(count(".issue-item")).toBe(2), WAIT);
    expect(expanded("acme/widgets")).toBe("false");
    expect(groupCount("acme/widgets")).toBe("3");

    await clickHeader("acme/widgets");
    await vi.waitFor(() => expect(count(".issue-item")).toBe(5), WAIT);

    await clickHeader("acme/widgets");
    mounted.unmount();
    mounted = await mountBrowserApp("/issues", { overrides: listOverrides() });

    await vi.waitFor(() => expect(expanded("acme/widgets")).toBe("false"), WAIT);
    expect(count(".issue-item")).toBe(2);
  });
});
