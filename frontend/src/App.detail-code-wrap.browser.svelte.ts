// Browser-tier coverage for the PR/issue detail code-fence wrap fix.
//
// A long unbroken code-fence line in a pull request or issue body must wrap
// inside the detail panel instead of overflowing horizontally and getting
// clipped. PullDetail.svelte / IssueDetail.svelte now apply
// `white-space: pre-wrap; overflow-wrap: anywhere` to their `.markdown-body`
// `pre`/`code` at every width; previously that lived only inside each
// component's `max-width: 640px` mobile media query, so the same components
// clipped long lines whenever they rendered in a narrow panel at a
// desktop-class width -- most visibly the workspace right sidebar, which
// embeds these very components.
//
// The viewport is a 1280px desktop window, above the 640px mobile breakpoint,
// so the pre-existing mobile rule is inactive and only the new all-width rule
// can keep the ~400-char line from overflowing the ~800px detail column. The
// assertion is computed layout (scrollWidth vs clientWidth and the resolved
// white-space), a real-browser-only concern, so it belongs in the browser tier
// rather than jsdom. The app is mounted for real with the detail mocked at the
// fetch boundary.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;

// One token with no internal break opportunities (no spaces/hyphens). At
// 400 chars it is far wider than the detail content column, so it can only fit
// without horizontal overflow when `overflow-wrap: anywhere` lets it wrap.
const LONG_TOKEN = "abcdefghij".repeat(40);
const FENCE = "```";
const CODE_BODY = `${FENCE}\n${LONG_TOKEN}\n${FENCE}`;

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

async function waitForBodyPre(rootSelector: string): Promise<HTMLElement> {
  await vi.waitFor(() => {
    const el = document.querySelector(`${rootSelector} .markdown-body pre`);
    expect(el).not.toBeNull();
    // Guard against a falsely-passing empty/short <pre>: the long token has to
    // be in the DOM for the overflow check to mean anything.
    expect((el?.textContent ?? "").replace(/\s/g, "").length).toBeGreaterThanOrEqual(300);
  }, WAIT);
  const pre = document.querySelector(`${rootSelector} .markdown-body pre`);
  if (!pre) throw new Error(`no <pre> rendered under ${rootSelector} .markdown-body`);
  return pre as HTMLElement;
}

function assertWrapsWithoutOverflow(pre: HTMLElement): void {
  // pre-wrap is the mechanism; scrollWidth <= clientWidth is the behavior it
  // buys. An unwrapped `white-space: pre` line would make the content wider
  // than the (clipped/scrollable) client box.
  expect(getComputedStyle(pre).whiteSpace).toBe("pre-wrap");
  expect(pre.scrollWidth).toBeLessThanOrEqual(pre.clientWidth + 1);
}

describe("PR/issue detail wraps long code-fence lines", () => {
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

  it("wraps an unbroken code-fence line in the pull request body", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/1", {
      overrides: [pullRoute("acme", "widgets", 1, CODE_BODY)],
    });
    assertWrapsWithoutOverflow(await waitForBodyPre(".pull-detail"));
  });

  it("wraps an unbroken code-fence line in the issue body", async () => {
    mounted = await mountBrowserApp("/issues/github/acme/widgets/7", {
      overrides: [issueRoute("acme", "widgets", 7, CODE_BODY)],
    });
    assertWrapsWithoutOverflow(await waitForBodyPre(".issue-detail"));
  });
});
