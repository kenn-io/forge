import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";

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

    // Self-link in the header ("Open on GitHub" for #205) does not preview the open PR itself.
    const selfHeaderLink = document.querySelector<HTMLAnchorElement>('.pull-detail a.gh-link[href*="/pull/205"]')!;
    expect(selfHeaderLink).toBeTruthy();
    dispatchPointer(selfHeaderLink, "pointerover");
    await new Promise((resolve) => setTimeout(resolve, 550));
    expect(popoverEl()).toBeNull();
  });

  it("consumes Escape only while visible, preserving pull details and the Activity drawer until the next press", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/205", { overrides: overrides() });
    await vi.waitFor(() => expect(detailTitle()).toContain("Enable shared retry policy"), WAIT);

    const link = document.querySelector<HTMLAnchorElement>('.markdown-body a.item-ref[data-number="104"]')!;
    dispatchPointer(link, "pointerover");
    await vi.waitFor(() => expect(popoverEl()).not.toBeNull(), WAIT);

    // First Escape closes only the preview; pull detail route stays intact.
    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => expect(popoverEl()).toBeNull(), WAIT);
    expect(window.location.pathname).toBe("/pulls/github/acme/widgets/205");
    expect(detailTitle()).toContain("Enable shared retry policy");

    // Second Escape runs the page's normal Escape shortcut back to /pulls.
    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => expect(window.location.pathname).toBe("/pulls"), WAIT);

    mounted.unmount();
    mounted = null;
    await resetKeyboardModuleState();

    // Activity drawer: first Escape closes only the preview, second closes the drawer.
    mounted = await mountBrowserApp(
      "/?selected=pr:205&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets",
      {
        overrides: [
          ...overrides(),
          (req) =>
            req.method === "GET" && req.url.pathname === "/api/v1/activity"
              ? jsonResponse({ items: [], total: 0, has_more: false })
              : null,
        ],
      },
    );
    await vi.waitFor(() => expect(detailTitle()).toContain("Enable shared retry policy"), WAIT);
    const drawerLink = document.querySelector<HTMLAnchorElement>('.markdown-body a.item-ref[data-number="104"]')!;
    dispatchPointer(drawerLink, "pointerover");
    await vi.waitFor(() => expect(popoverEl()).not.toBeNull(), WAIT);

    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => expect(popoverEl()).toBeNull(), WAIT);
    expect(window.location.search).toContain("selected=pr");
    expect(detailTitle()).toContain("Enable shared retry policy");

    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => expect(document.querySelector(".pull-detail")).toBeNull(), WAIT);
  });

  it("associates keyboard-focused previews via aria-describedby after dwell and restores prior attributes on close", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/205", { overrides: overrides() });
    await vi.waitFor(() => expect(detailTitle()).toContain("Enable shared retry policy"), WAIT);

    const link = document.querySelector<HTMLAnchorElement>('.markdown-body a.item-ref[data-number="104"]')!;
    const existingHintId = "existing-link-hint";
    link.setAttribute("aria-describedby", existingHintId);

    // Move focus onto the link via real keyboard navigation so :focus-visible matches.
    const beforeBtn = document.createElement("button");
    beforeBtn.type = "button";
    beforeBtn.textContent = "Before link";
    link.parentElement!.insertBefore(beforeBtn, link);
    beforeBtn.focus();

    await userEvent.tab();
    expect(document.activeElement).toBe(link);

    // Moving away before the focus dwell expires neither opens nor fetches.
    const initialRequests = mounted.api.requests.filter((r) =>
      r.url.searchParams.getAll("numbers").includes("104"),
    ).length;
    await new Promise((resolve) => setTimeout(resolve, 100));
    await userEvent.tab();
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(popoverEl()).toBeNull();
    expect(mounted.api.requests.filter((r) => r.url.searchParams.getAll("numbers").includes("104")).length).toBe(
      initialRequests,
    );

    // Resting keyboard focus opens the card and appends its id to aria-describedby without role="dialog".
    beforeBtn.focus();
    await userEvent.tab();
    expect(document.activeElement).toBe(link);
    await vi.waitFor(() => expect(popoverEl()).not.toBeNull(), WAIT);

    const card = popoverEl()!;
    expect(card.getAttribute("role")).toBe("group");
    expect(card.id).toBeTruthy();
    const describedByTokens = (link.getAttribute("aria-describedby") ?? "").split(/\s+/);
    expect(describedByTokens).toContain(existingHintId);
    expect(describedByTokens).toContain(card.id);

    // Escape dismisses the preview, keeps focus on the link, and restores the original aria-describedby.
    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => expect(popoverEl()).toBeNull(), WAIT);
    expect(document.activeElement).toBe(link);
    expect(link.getAttribute("aria-describedby")).toBe(existingHintId);
  });

  it("anchors wrapped multi-line links beside the hovered line fragment and clamps to viewport edges", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/205", { overrides: overrides() });
    await vi.waitFor(() => expect(detailTitle()).toContain("Enable shared retry policy"), WAIT);

    const link = document.querySelector<HTMLAnchorElement>('.markdown-body a.item-ref[data-number="104"]')!;
    const paragraph = link.closest("p")!;
    paragraph.style.width = "160px";
    paragraph.style.overflowWrap = "anywhere";
    paragraph.style.wordBreak = "break-all";

    const rects = Array.from(link.getClientRects()).filter((r) => r.width > 0 && r.height > 0);
    expect(rects.length).toBeGreaterThan(1);

    const firstLine = rects[0]!;
    const lastLine = rects[rects.length - 1]!;
    expect(lastLine.top).toBeGreaterThan(firstLine.top);

    dispatchPointer(link, "pointerover", {
      clientX: lastLine.left + Math.min(8, lastLine.width / 2),
      clientY: lastLine.top + lastLine.height / 2,
    });
    await vi.waitFor(() => expect(popoverEl()).not.toBeNull(), WAIT);

    const cardRect = popoverEl()!.getBoundingClientRect();
    // Card opens adjacent to the hovered last line (below or above it by ~8px gap),
    // not anchored to the first line's edge.
    const gapBelowLastLine = Math.abs(cardRect.top - lastLine.bottom);
    const gapAboveLastLine = Math.abs(lastLine.top - cardRect.bottom);
    expect(Math.min(gapBelowLastLine, gapAboveLastLine)).toBeLessThanOrEqual(14);
    expect(cardRect.left).toBeGreaterThanOrEqual(8);
    expect(cardRect.right).toBeLessThanOrEqual(window.innerWidth - 8);
    expect(cardRect.top).toBeGreaterThanOrEqual(8);
    expect(cardRect.bottom).toBeLessThanOrEqual(window.innerHeight - 8);

    // Close and verify viewport-edge clamping when a link sits near the bottom-right corner.
    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => expect(popoverEl()).toBeNull(), WAIT);

    const cornerLink = document.createElement("a");
    cornerLink.className = "item-ref";
    cornerLink.href = "https://github.com/acme/widgets/pull/104";
    cornerLink.dataset.provider = "github";
    cornerLink.dataset.platformHost = "github.com";
    cornerLink.dataset.owner = "acme";
    cornerLink.dataset.name = "widgets";
    cornerLink.dataset.repoPath = "acme/widgets";
    cornerLink.dataset.number = "104";
    cornerLink.dataset.itemType = "pr";
    cornerLink.textContent = "acme/widgets#104";
    cornerLink.style.position = "fixed";
    cornerLink.style.right = "12px";
    cornerLink.style.bottom = "12px";
    document.body.appendChild(cornerLink);

    try {
      dispatchPointer(cornerLink, "pointerover");
      await vi.waitFor(() => expect(popoverEl()).not.toBeNull(), WAIT);
      const cornerRect = cornerLink.getBoundingClientRect();
      const clampedCard = popoverEl()!.getBoundingClientRect();
      // Flips above the bottom-edge link and clamps inside the right edge.
      expect(clampedCard.bottom).toBeLessThanOrEqual(cornerRect.top + 1);
      expect(clampedCard.right).toBeLessThanOrEqual(window.innerWidth - 8);
      expect(clampedCard.left).toBeGreaterThanOrEqual(8);
      expect(clampedCard.top).toBeGreaterThanOrEqual(8);
    } finally {
      cornerLink.remove();
    }
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
