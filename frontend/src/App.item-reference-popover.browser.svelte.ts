import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;

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
    },
  };
}

function makePullRow(overrides: Record<string, unknown> = {}) {
  return {
    ID: 104,
    RepoID: 1,
    GitHubID: 90104,
    Number: 104,
    URL: "https://github.com/acme/widgets/pull/104",
    Title: "Configure retry backoff pool",
    Author: "alice",
    AuthorDisplayName: "Alice",
    State: "open",
    IsDraft: false,
    IsLocked: false,
    MergeableState: "clean",
    Body: "<!-- template -->\nSwitches the request client to **bounded jitter** with shared retry budgets across workers.",
    HeadBranch: "feat/retry-backoff",
    BaseBranch: "main",
    HeadRepoCloneURL: "",
    Additions: 142,
    Deletions: 27,
    FilesChanged: 4,
    MergeCommitSHA: "",
    CommentCount: 5,
    ReviewDecision: "APPROVED",
    CIStatus: "success",
    CIChecksJSON: JSON.stringify([
      { app: "GitHub Actions", name: "test", status: "completed", conclusion: "success", url: "" },
      { app: "GitHub Actions", name: "lint", status: "completed", conclusion: "success", url: "" },
    ]),
    CIHadPending: false,
    MergeRules: "",
    CreatedAt: "2026-03-29T14:00:00Z",
    UpdatedAt: "2026-03-30T14:00:00Z",
    LastActivityAt: "2026-03-30T14:00:00Z",
    MergedAt: null,
    ClosedAt: null,
    Starred: false,
    detail_loaded: true,
    head_repo_kind: "same_repo",
    labels: [{ name: "reliability", color: "0e8a16" }],
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    platform_head_sha: "0104aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0104",
    repo: repoRef("acme", "widgets"),
    worktree_links: [],
    ...overrides,
  };
}

const currentPull = makePullRow({
  ID: 205,
  GitHubID: 90205,
  Number: 205,
  URL: "https://github.com/acme/widgets/pull/205",
  Title: "Enable shared retry policy",
  Body: "Relies on https://github.com/acme/widgets/pull/104 and https://github.com/external/untracked/pull/99.",
  ReviewDecision: "",
  CIChecksJSON: "[]",
  labels: [],
});

const targetPull = makePullRow();

function detailEnvelope(pr: ReturnType<typeof makePullRow>) {
  return {
    merge_request: pr,
    repo: pr.repo,
    events: [],
    repo_owner: pr.repo_owner,
    repo_name: pr.repo_name,
    platform_host: pr.platform_host,
    platform_head_sha: pr.platform_head_sha,
    reviewed_head_sha: pr.platform_head_sha,
    detail_loaded: true,
    detail_fetched_at: "2026-03-30T14:00:00Z",
    worktree_links: [],
  };
}

function overrides(): MockRouteOverride[] {
  return [
    (req) =>
      req.url.pathname === "/api/v1/repos"
        ? jsonResponse([
            {
              ID: 1,
              PlatformRepoID: 1001,
              Owner: "acme",
              Name: "widgets",
              Platform: "github",
              PlatformHost: "github.com",
              AllowSquashMerge: true,
              AllowMergeCommit: true,
              AllowRebaseMerge: true,
              ViewerCanMerge: true,
              LastSyncStartedAt: null,
              LastSyncCompletedAt: null,
              LastSyncError: "",
              CreatedAt: "2026-03-01T00:00:00Z",
            },
          ])
        : null,
    (req) => {
      if (req.method !== "GET" || req.url.pathname !== "/api/v1/pulls") return null;
      const numbers = req.url.searchParams.getAll("numbers");
      if (numbers.includes("104")) return jsonResponse([targetPull]);
      return jsonResponse([currentPull, targetPull]);
    },
    (req) =>
      /^\/api\/v1\/pulls\/github\/acme\/widgets\/205(\/sync\/async)?$/.test(req.url.pathname)
        ? jsonResponse(detailEnvelope(currentPull))
        : null,
    (req) =>
      /^\/api\/v1\/pulls\/github\/acme\/widgets\/104(\/sync\/async)?$/.test(req.url.pathname)
        ? jsonResponse(detailEnvelope(targetPull))
        : null,
    (req) =>
      req.method === "POST" && req.url.pathname === "/api/v1/repo/github/acme/widgets/resolve/104"
        ? jsonResponse({ number: 104, item_type: "pr", repo_tracked: true })
        : null,
  ];
}

function detailTitle(): string {
  return document.querySelector(".pull-detail .detail-title")?.textContent ?? "";
}

function popoverEl(): HTMLElement | null {
  return document.querySelector<HTMLElement>('[data-testid="item-ref-popover"]');
}

function dispatchPointer(target: Element, type: string, init: Partial<PointerEventInit> = {}): void {
  const rect = target.getBoundingClientRect();
  target.dispatchEvent(
    new PointerEvent(type, {
      bubbles: true,
      cancelable: true,
      pointerType: "mouse",
      clientX: rect.left + Math.min(12, rect.width / 2),
      clientY: rect.top + rect.height / 2,
      ...init,
    }),
  );
}

describe("item reference hover popover", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    vi.restoreAllMocks();
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("shows PR title, description excerpt, status, branches, and diff stats after hovering a tracked link", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/205", { overrides: overrides() });
    await vi.waitFor(() => expect(detailTitle()).toContain("Enable shared retry policy"), WAIT);

    const link = document.querySelector<HTMLAnchorElement>('.markdown-body a.item-ref[data-number="104"]')!;
    expect(link).toBeTruthy();

    dispatchPointer(link, "pointerover");
    // Does not open instantaneously on initial hover.
    expect(popoverEl()).toBeNull();

    // Opens after hover dwell with full PR summary and status.
    await vi.waitFor(() => expect(popoverEl()).not.toBeNull(), WAIT);
    const card = popoverEl()!;
    expect(card.textContent).toContain("acme/widgets");
    expect(card.textContent).toContain("#104");
    expect(card.textContent).toContain("Configure retry backoff pool");
    expect(card.textContent).toContain("Switches the request client to bounded jitter");
    expect(card.textContent).toContain("Open");
    expect(card.textContent).toContain("Approved");
    expect(card.textContent).toContain("feat/retry-backoff");
    expect(card.textContent).toContain("+142");
    expect(card.textContent).toContain("−27");
    expect(card.textContent).toContain("reliability");
    expect(card.textContent).toContain("alice");

    // Hover bridge: leaving the link into the popover keeps it open, and leaving the popover closes it.
    dispatchPointer(link, "pointerout", { relatedTarget: card });
    dispatchPointer(card, "pointerenter");
    await new Promise((resolve) => setTimeout(resolve, 260));
    expect(popoverEl()).not.toBeNull();

    dispatchPointer(card, "pointerleave", { relatedTarget: document.body });
    await vi.waitFor(() => expect(popoverEl()).toBeNull(), WAIT);

    // Re-hovering opens immediately from cache after dwell, and Escape dismisses it.
    dispatchPointer(link, "pointerover");
    await vi.waitFor(() => expect(popoverEl()).not.toBeNull(), WAIT);
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await vi.waitFor(() => expect(popoverEl()).toBeNull(), WAIT);
  });

  it("does not open or fetch when moving off a link before the hover delay or on touch pointers", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/205", { overrides: overrides() });
    await vi.waitFor(() => expect(detailTitle()).toContain("Enable shared retry policy"), WAIT);

    const link = document.querySelector<HTMLAnchorElement>('.markdown-body a.item-ref[data-number="104"]')!;
    const initialRequests = mounted.api.requests.filter((r) =>
      r.url.searchParams.getAll("numbers").includes("104"),
    ).length;

    // Quick pass-through: enter and leave before the 450ms dwell expires.
    dispatchPointer(link, "pointerover");
    await new Promise((resolve) => setTimeout(resolve, 100));
    dispatchPointer(link, "pointerout", { relatedTarget: document.body });
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(popoverEl()).toBeNull();

    // Touch pointers never trigger hover previews.
    dispatchPointer(link, "pointerover", { pointerType: "touch" });
    await new Promise((resolve) => setTimeout(resolve, 550));
    expect(popoverEl()).toBeNull();

    const previewRequests = mounted.api.requests.filter((r) =>
      r.url.searchParams.getAll("numbers").includes("104"),
    ).length;
    expect(previewRequests).toBe(initialRequests);
  });

  it("never opens a popover or fetches preview data for an untracked repository link", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/205", { overrides: overrides() });
    await vi.waitFor(() => expect(detailTitle()).toContain("Enable shared retry policy"), WAIT);

    const untrackedLink = document.querySelector<HTMLAnchorElement>(
      '.markdown-body a.item-ref[data-owner="external"]',
    )!;
    expect(untrackedLink).toBeTruthy();

    dispatchPointer(untrackedLink, "pointerover");
    await new Promise((resolve) => setTimeout(resolve, 600));

    expect(popoverEl()).toBeNull();
    expect(
      mounted.api.requests.some((r) => r.url.pathname.includes("external") || r.url.search.includes("external")),
    ).toBe(false);
  });

  it("navigates to the linked PR and closes when clicking the title inside the popover", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/205", { overrides: overrides() });
    await vi.waitFor(() => expect(detailTitle()).toContain("Enable shared retry policy"), WAIT);

    const link = document.querySelector<HTMLAnchorElement>('.markdown-body a.item-ref[data-number="104"]')!;
    dispatchPointer(link, "pointerover");
    await vi.waitFor(() => expect(popoverEl()).not.toBeNull(), WAIT);

    const titleLink = popoverEl()!.querySelector<HTMLAnchorElement>(".item-ref-popover__title")!;
    titleLink.click();

    await vi.waitFor(() => expect(popoverEl()).toBeNull(), WAIT);
    await vi.waitFor(() => expect(window.location.pathname).toBe("/pulls/github/acme/widgets/104"), WAIT);
    await vi.waitFor(() => expect(detailTitle()).toContain("Configure retry backoff pool"), WAIT);
  });
});
