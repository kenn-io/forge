// Browser-tier reimplementation of frontend/tests/e2e-full/stack-status.spec.ts.
// The stack panel is full-stack rendering driven by the merge-request detail's
// `stack` field: a 3-member auth stack (acme/tools #10/#11/#12) whose downstack
// conflict propagates so every member renders a conflict label, while the
// summary counts only the conflicts below the current member. The app is mounted
// for real through the browser harness with the detail responses mocked at the
// fetch boundary, so the rendered summary, member rows, base row, and the URL
// after member navigation are the genuine outputs of the stack data.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource. Member-row text and the base-row shape are
// exact-DOM assertions the page locator API does not expose, so they stay as
// scoped querySelector against the real DOM, wrapped in vi.waitFor for the async
// render/navigation chain.
//
// The member data mirrors the seed in internal/testutil/fixtures.go. The raw DB
// marks only #10 dirty, but the stack API propagates downstack conflicts upward
// (the passing e2e asserts "× Conflicts" three times), so the response members
// all carry mergeable_state "dirty" -- the response shape this UI renders.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;

const toolsRepo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/tools",
  owner: "acme",
  name: "tools",
  capabilities: {
    read_repositories: true,
    read_merge_requests: true,
    read_issues: true,
    read_comments: true,
    read_releases: true,
    read_ci: true,
    read_labels: true,
    comment_mutation: true,
    state_mutation: true,
    merge_mutation: true,
    label_mutation: true,
    review_mutation: true,
    workflow_approval: true,
    ready_for_review: true,
    issue_mutation: true,
  },
};

// The auth stack as the /stack-derived response renders it: every member dirty
// so each row shows the "× Conflicts" label; positions 1..3 bottom-to-top.
function authStack() {
  return {
    stack_id: 1,
    stack_name: "auth",
    position: 2,
    size: 3,
    health: "downstack_conflict",
    members: [
      {
        number: 10,
        title: "Auth: extract token refresh helper",
        state: "open",
        ci_status: "success",
        review_decision: "APPROVED",
        mergeable_state: "dirty",
        position: 1,
        is_draft: false,
        base_branch: "main",
        blocked_by: null,
      },
      {
        number: 11,
        title: "Auth: add retry with backoff",
        state: "open",
        ci_status: "success",
        review_decision: "",
        mergeable_state: "dirty",
        position: 2,
        is_draft: false,
        base_branch: "feat/auth-base",
        blocked_by: 10,
      },
      {
        number: 12,
        title: "Auth: error handling UI",
        state: "open",
        ci_status: "pending",
        review_decision: "",
        mergeable_state: "dirty",
        position: 3,
        is_draft: false,
        base_branch: "feat/auth-retry",
        blocked_by: 11,
      },
    ],
  };
}

function toolsStackMember(number: number) {
  return authStack().members.find((member) => member.number === number)!;
}

function toolsPullDetail(number: number) {
  const member = toolsStackMember(number);
  return {
    merge_request: {
      ID: 2000 + number,
      RepoID: 2,
      GitHubID: 2010 + number,
      Number: number,
      URL: `https://github.com/acme/tools/pull/${number}`,
      Title: member.title,
      Author: "alice",
      State: "open",
      IsDraft: false,
      IsLocked: false,
      MergeableState: "dirty",
      Body: "",
      HeadBranch: member.base_branch === "main" ? "feat/auth-base" : `feat/auth-${number}`,
      BaseBranch: member.base_branch,
      Additions: 50,
      Deletions: 5,
      CommentCount: 0,
      ReviewDecision: member.review_decision,
      CIStatus: member.ci_status,
      CIChecksJSON: "[]",
      CreatedAt: "2026-03-29T14:00:00Z",
      UpdatedAt: "2026-03-30T14:00:00Z",
      LastActivityAt: "2026-03-30T14:00:00Z",
      MergedAt: null,
      ClosedAt: null,
      KanbanStatus: "reviewing",
      Starred: false,
      repo_owner: "acme",
      repo_name: "tools",
      platform_host: "github.com",
      platform_head_sha: "a0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0",
      repo: toolsRepo,
      worktree_links: [],
    },
    repo: toolsRepo,
    stack: authStack(),
    repo_owner: "acme",
    repo_name: "tools",
    platform_host: "github.com",
    platform_head_sha: "a0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0",
    reviewed_head_sha: "a0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0",
    detail_loaded: true,
    detail_fetched_at: "2026-03-30T14:00:00Z",
    worktree_links: [],
  };
}

// Serve every acme/tools stack member detail by number.
function toolsStackRoutes(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET") return null;
    const match = req.url.pathname.match(/^\/api\/v1\/pulls\/github\/acme\/tools\/(\d+)$/);
    if (!match) return null;
    return jsonResponse(toolsPullDetail(Number(match[1])));
  };
}

function memberLinkTexts(): string[] {
  return Array.from(document.querySelectorAll(".stack-panel .stack-member-link")).map((el) =>
    (el.textContent ?? "").replace(/\s+/g, " ").trim(),
  );
}

function conflictLabelCount(): number {
  return Array.from(document.querySelectorAll(".stack-panel .stack-status-label")).filter(
    (el) => (el.textContent ?? "").trim() === "× Conflicts",
  ).length;
}

function stackSummaryText(): string {
  return document.querySelector(".stack-panel .stack-summary")?.textContent?.trim() ?? "";
}

describe("stack status panel", () => {
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

  it("renders the stack panel and a passive base row from the full-stack data", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/tools/11", {
      overrides: [toolsStackRoutes()],
    });

    await expect
      .element(page.getByText("This branch has conflicts that must be resolved before merging."))
      .toBeVisible();

    await page.getByTestId("stack-chip").click();

    await vi.waitFor(() => {
      expect(stackSummaryText()).toBe("3 PRs · current 2/3 · downstack conflict");
    }, WAIT);
    expect(conflictLabelCount()).toBe(3);
    expect(memberLinkTexts()).toEqual([
      "#12 Auth: error handling UI",
      "#11 Auth: add retry with backoff",
      "#10 Auth: extract token refresh helper",
    ]);

    const baseRow = document.querySelector(".stack-panel .stack-row--base");
    expect(baseRow?.getAttribute("aria-label")).toBe("Stack base main");
    expect(baseRow?.querySelector(".stack-base-name")?.textContent?.trim()).toBe("main");
    expect(baseRow?.querySelectorAll(".stack-member-link").length).toBe(0);
    expect(window.location.pathname).toBe("/pulls/github/acme/tools/11");
  });

  it("preserves the focus route when navigating to a stack member", async () => {
    mounted = await mountBrowserApp("/focus/pulls/github/acme/tools/11", {
      overrides: [toolsStackRoutes()],
    });

    await expect.element(page.getByTestId("stack-chip")).toBeVisible();
    await page.getByTestId("stack-chip").click();
    await page.getByRole("button", { name: "#10 Auth: extract token refresh helper" }).click();

    await vi.waitFor(() => {
      expect(window.location.pathname).toBe("/focus/pulls/github/acme/tools/10");
    }, WAIT);
    await vi.waitFor(() => {
      expect(stackSummaryText()).toContain("3 PRs · current 1/3");
    }, WAIT);
  });
});
