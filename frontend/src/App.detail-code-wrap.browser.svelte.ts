// Browser-tier coverage for the PR/issue detail code-fence wrap fix.
//
// Two behaviors the fix must keep separate:
//   1. A long unbroken line inside a fenced code block must wrap so the <pre>
//      does not overflow horizontally and get clipped by the detail panel.
//   2. A long inline-code identifier inside a markdown table must NOT wrap, so
//      the column stays wide and the table scrolls (the app.css table-cell
//      reset), matching how github.com renders it.
//
// PullDetail.svelte / IssueDetail.svelte scope the wrap to `.markdown-body pre`
// (white-space/overflow-wrap/word-break inherit to the inner <code>), so fenced
// code wraps while inline code -- including inline code in table cells -- is
// untouched. A previous revision applied the wrap to `.markdown-body code` too,
// which overrode the table-cell reset and let desktop tables soft-wrap; this
// test guards both halves so that regression cannot return.
//
// The viewport is a 1280px desktop window, above the 640px mobile breakpoint,
// so the component's pre-existing mobile rule is inactive and only the new
// all-width rule is in play. The assertions are computed layout / resolved
// white-space, a real-browser-only concern, so this lives in the Vitest browser
// tier. The app is mounted for real with the detail mocked at the fetch
// boundary.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;

// A fenced line with no internal break opportunities. At 400 chars it is far
// wider than the detail content column, so it can only fit without horizontal
// overflow when the fenced-code wrap lets it break anywhere.
const FENCED_LINE = "abcdefghij".repeat(40);
// A long inline-code identifier placed in a table cell; it must stay on one
// line so the column grows and the table scrolls.
const TABLE_TOKEN = "z".repeat(80);
const FENCE = "```";

const MIXED_BODY = [
  "| Package | Version |",
  "| --- | --- |",
  "| `" + TABLE_TOKEN + "` | `1.2.3` |",
  "",
  FENCE,
  FENCED_LINE,
  FENCE,
  "",
].join("\n");

function repoRef(owner: string, name: string) {
  return {
    provider: "github",
    platform_host: "github.com",
    repo_path: `${owner}/${name}`,
    owner,
    name,
    capabilities: {
      read_repositories: true,
      read_merge_requests: true,
      read_issues: true,
      read_comments: true,
      state_mutation: true,
      comment_mutation: true,
    },
  };
}

function pullDetail(owner: string, name: string, number: number, body: string) {
  const headSHA = `${number}aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa${number}`;
  return {
    merge_request: {
      ID: 1000 + number,
      RepoID: 1,
      GitHubID: 1100 + number,
      Number: number,
      URL: `https://github.com/${owner}/${name}/pull/${number}`,
      Title: "Long code fence",
      Author: "alice",
      State: "open",
      IsDraft: false,
      MergeableState: "clean",
      Body: body,
      HeadBranch: "feature/x",
      BaseBranch: "main",
      Additions: 1,
      Deletions: 0,
      CommentCount: 0,
      ReviewDecision: "",
      CIStatus: "success",
      CIChecksJSON: "[]",
      CreatedAt: "2026-02-28T14:00:00Z",
      UpdatedAt: "2026-03-02T14:00:00Z",
      LastActivityAt: "2026-03-02T14:00:00Z",
      MergedAt: null,
      ClosedAt: null,
      KanbanStatus: "new",
      Starred: false,
      repo_owner: owner,
      repo_name: name,
      platform_host: "github.com",
      platform_head_sha: headSHA,
      repo: repoRef(owner, name),
      worktree_links: [],
    },
    repo: repoRef(owner, name),
    events: [],
    repo_owner: owner,
    repo_name: name,
    platform_host: "github.com",
    platform_head_sha: headSHA,
    reviewed_head_sha: headSHA,
    detail_loaded: true,
    detail_fetched_at: "2026-03-02T14:00:00Z",
    worktree_links: [],
  };
}

function issueDetail(owner: string, name: string, number: number, body: string) {
  return {
    issue: {
      ID: 2000 + number,
      RepoID: 1,
      GitHubID: 2200 + number,
      Number: number,
      URL: `https://github.com/${owner}/${name}/issues/${number}`,
      Title: "Long code fence",
      Author: "alice",
      State: "open",
      Body: body,
      CommentCount: 0,
      LabelsJSON: "[]",
      CreatedAt: "2026-03-28T14:00:00Z",
      UpdatedAt: "2026-03-30T14:00:00Z",
      LastActivityAt: "2026-03-30T14:00:00Z",
      ClosedAt: null,
      Starred: false,
    },
    events: [],
    platform_host: "github.com",
    repo_owner: owner,
    repo_name: name,
    detail_loaded: true,
    detail_fetched_at: "2026-03-30T14:00:00Z",
  };
}

function pullRoute(owner: string, name: string, number: number, body: string): MockRouteOverride {
  const re = new RegExp(`^/api/v1/(host/[^/]+/)?pulls/github/${owner}/${name}/${number}(/sync/async)?$`);
  return (req) => (re.test(req.url.pathname) ? jsonResponse(pullDetail(owner, name, number, body)) : null);
}

function issueRoute(owner: string, name: string, number: number, body: string): MockRouteOverride {
  const hosted = new RegExp(`^/api/v1/(host/[^/]+/)?issues/github/${owner}/${name}/${number}(/sync/async)?$`);
  const legacy = new RegExp(`^/api/v1/repos/${owner}/${name}/issues/${number}$`);
  return (req) =>
    hosted.test(req.url.pathname) || legacy.test(req.url.pathname)
      ? jsonResponse(issueDetail(owner, name, number, body))
      : null;
}

const squashedLen = (el: Element): number => (el.textContent ?? "").replace(/\s/g, "").length;

// Wait for a node carrying its long token: guards against a falsely-passing
// empty/short element before the computed-style/layout checks run.
async function waitForEl(selector: string, minLen: number): Promise<HTMLElement> {
  await vi.waitFor(() => {
    const el = document.querySelector(selector);
    expect(el).not.toBeNull();
    expect(squashedLen(el as Element)).toBeGreaterThanOrEqual(minLen);
  }, WAIT);
  const el = document.querySelector(selector);
  if (!el) throw new Error(`no element rendered for ${selector}`);
  return el as HTMLElement;
}

async function assertDetailCodeLayout(root: string): Promise<void> {
  // Fenced code wraps: pre-wrap is the mechanism, scrollWidth <= clientWidth is
  // the behavior it buys (an unwrapped `white-space: pre` line would make the
  // content wider than the clipped/scrollable client box).
  const pre = await waitForEl(`${root} .markdown-body pre`, 300);
  expect(getComputedStyle(pre).whiteSpace).toBe("pre-wrap");
  expect(pre.scrollWidth).toBeLessThanOrEqual(pre.clientWidth + 1);

  // Inline code in a table keeps the app.css table-cell reset: it must not
  // inherit the fenced-code wrap, so the long identifier stays on one line.
  const tableCode = await waitForEl(`${root} .markdown-body table td code`, 60);
  expect(getComputedStyle(tableCode).whiteSpace).toBe("normal");
}

describe("PR/issue detail code-fence wrapping", () => {
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

  it("wraps fenced code but not table inline code in the pull request body", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/1", {
      overrides: [pullRoute("acme", "widgets", 1, MIXED_BODY)],
    });
    await assertDetailCodeLayout(".pull-detail");
  });

  it("wraps fenced code but not table inline code in the issue body", async () => {
    mounted = await mountBrowserApp("/issues/github/acme/widgets/7", {
      overrides: [issueRoute("acme", "widgets", 7, MIXED_BODY)],
    });
    await assertDetailCodeLayout(".issue-detail");
  });
});
