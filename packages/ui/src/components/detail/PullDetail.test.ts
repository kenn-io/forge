import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { DiffResult, PullDetail } from "../../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, STORES_KEY, UI_CONFIG_KEY } from "../../context.js";
import { createDetailActivityViewStore } from "../../stores/detail-activity-view.svelte.js";
import { createDetailStore } from "../../stores/detail.svelte.js";
import { dismissFlash, getFlashes } from "../../stores/flash.svelte.js";
import { resetWorkspaceCreatePendingForTest } from "../../stores/workspace-create-pending.svelte.js";
import type { MiddlemanClient } from "../../types.js";
import type { InlineWorkspaceController, WorkspaceItemIdentity } from "../../workspace-inline.js";
import { openLabelPickerFor } from "./labelPickerCommand.js";
import { createTestController } from "../workspace/WorkspaceDockPanelTestController.svelte.js";

// The pending-create store is module-scoped so it can survive component
// remounts; tests that leave a deferred create unresolved must not leak
// that pending identity into later tests.
afterEach(resetWorkspaceCreatePendingForTest);

const markdownMockState = vi.hoisted(() => ({
  pending: false,
  pendingPromise: new Promise<string>(() => undefined),
}));

const clipboardMockState = vi.hoisted(() => ({
  resolvers: [] as Array<(ok: boolean) => void>,
}));

vi.mock("@kenn-io/kit-ui", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@kenn-io/kit-ui")>();
  return {
    ...actual,
    copyToClipboard: vi.fn(() => new Promise<boolean>((resolve) => clipboardMockState.resolvers.push(resolve))),
  };
});

vi.mock("../../utils/markdown.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../utils/markdown.js")>();
  return {
    ...actual,
    renderMarkdown: vi.fn((raw: string, repo?: unknown, opts?: unknown) =>
      markdownMockState.pending
        ? markdownMockState.pendingPromise
        : actual.renderMarkdown(
            raw,
            repo as Parameters<typeof actual.renderMarkdown>[1],
            opts as Parameters<typeof actual.renderMarkdown>[2],
          ),
    ),
  };
});

import PullDetailComponent from "./PullDetail.svelte";

const capabilities = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  read_labels: false,
  comment_mutation: false,
  state_mutation: true,
  merge_mutation: false,
  review_mutation: false,
  workflow_approval: false,
  ready_for_review: false,
  issue_mutation: false,
  label_mutation: false,
};

function reviewEvent(author: string, summary = "APPROVED", createdAt = "2026-05-01T12:00:00Z") {
  return {
    ID: Math.floor(Math.random() * 1_000_000),
    MergeRequestID: 1,
    PlatformID: 1,
    PlatformExternalID: "",
    EventType: "review",
    Author: author,
    Summary: summary,
    Body: "",
    MetadataJSON: "",
    CreatedAt: createdAt,
    DedupeKey: `review-${author}-${summary}-${createdAt}`,
  };
}

function pullDetail(): PullDetail {
  return {
    detail_loaded: true,
    detail_fetched_at: "2026-05-01T12:05:00Z",
    deferred_merge_pending: false,
    diff_head_sha: "head",
    merge_base_sha: "base",
    platform_base_sha: "base",
    platform_head_sha: "head",
    reviewed_head_sha: "head",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widget",
    warnings: [],
    workflow_approval: {
      count: 0,
      required: false,
      runs: [],
    },
    workspace: undefined,
    worktree_links: [],
    repo: {
      ID: 1,
      Owner: "acme",
      Name: "widget",
      Host: "github.com",
      PlatformHost: "github.com",
      Platform: "github",
      URL: "https://github.com/acme/widget",
      DefaultBranch: "main",
      IsArchived: false,
      AllowSquashMerge: false,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      capabilities,
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widget",
      repo_path: "acme/widget",
    },
    merge_request: {
      ID: 1,
      RepoID: 1,
      PlatformID: 100,
      PlatformExternalID: "PR_1",
      Number: 1,
      URL: "https://github.com/acme/widget/pull/1",
      Title: "Make approval counts visible",
      Author: "octocat",
      AuthorDisplayName: "Octocat",
      State: "open",
      IsDraft: false,
      IsLocked: false,
      Body: "",
      HeadBranch: "feature",
      BaseBranch: "main",
      HeadRepoCloneURL: "https://github.com/acme/widget.git",
      Additions: 0,
      Deletions: 0,
      CommentCount: 0,
      ReviewDecision: "APPROVED",
      CIStatus: "",
      CIChecksJSON: "",
      CIHadPending: false,
      CreatedAt: "2026-05-01T11:00:00Z",
      UpdatedAt: "2026-05-01T12:00:00Z",
      LastActivityAt: "2026-05-01T12:00:00Z",
      MergedAt: null,
      ClosedAt: null,
      MergeableState: "clean",
      DetailFetchedAt: "2026-05-01T12:05:00Z",
      KanbanStatus: "new",
      Starred: false,
      labels: [],
    },
    events: [
      reviewEvent("alice", "APPROVED", "2026-05-01T12:00:00Z"),
      reviewEvent("bob", "APPROVED", "2026-05-01T11:59:00Z"),
    ],
  };
}

function renderPullDetail(
  detail: PullDetail,
  repoSettings = {
    AllowSquashMerge: false,
    AllowMergeCommit: false,
    AllowRebaseMerge: false,
    ViewerCanMerge: true,
  },
  apiClient = {
    GET: vi.fn(async () => ({
      data: repoSettings,
    })),
    POST: vi.fn(async () => ({
      data: {},
    })),
  },
  options: {
    hideWorkspaceAction?: boolean;
    actions?: { pull: unknown[] };
    detailSyncing?: boolean;
    inlineWorkspace?: InlineWorkspaceController | null;
  } = {},
) {
  const actions = options.actions ?? { pull: [] };
  const detailStore = {
    loadDetail: vi.fn(async () => undefined),
    startDetailPolling: vi.fn(),
    stopDetailPolling: vi.fn(),
    getDetail: () => detail,
    isDetailLoading: () => false,
    getDetailError: () => null,
    isDetailSyncing: () => options.detailSyncing ?? false,
    getDetailLoaded: () => true,
    updateKanbanState: vi.fn(),
    toggleDetailPRStar: vi.fn(),
    updatePRContent: vi.fn(),
    refreshPendingCI: vi.fn(async () => undefined),
    syncDetailNow: vi.fn(async () => true),
    refreshDetailOnly: vi.fn(async () => undefined),
    editComment: vi.fn(),
    applyReviewSuggestions: vi.fn(async () => true),
  };
  const navigate = vi.fn();

  const rendered = render(PullDetailComponent, {
    props: {
      owner: "acme",
      name: "widget",
      number: detail.merge_request.Number,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      hideWorkspaceAction: options.hideWorkspaceAction ?? true,
      inlineWorkspace: options.inlineWorkspace ?? null,
    },
    context: new Map<symbol, unknown>([
      [API_CLIENT_KEY, apiClient],
      [
        STORES_KEY,
        {
          detail: detailStore,
          pulls: { loadPulls: vi.fn() },
          activity: { loadActivity: vi.fn() },
          detailActivityView: createDetailActivityViewStore(),
        },
      ],
      [ACTIONS_KEY, actions],
      [UI_CONFIG_KEY, { hideStar: true }],
      [NAVIGATE_KEY, navigate],
    ]),
  });
  return { ...rendered, detailStore, navigate };
}

function addReviewSuggestionToDetail(detail: PullDetail): void {
  detail.repo.capabilities.review_suggestion_application = true;
  detail.repo.operations = {
    apply_review_suggestion: {
      available: true,
    },
  } as unknown as PullDetail["repo"]["operations"];
  detail.events = [
    {
      ID: 501,
      MergeRequestID: detail.merge_request.ID,
      PlatformID: 501,
      PlatformExternalID: "review-comment-501",
      EventType: "review_comment",
      Author: "reviewer",
      Summary: "",
      Body: ["This can return directly.", "", "```suggestion", "return client.publishThreads();", "```"].join("\n"),
      MetadataJSON: "",
      CreatedAt: "2026-05-01T12:01:00Z",
      DedupeKey: "review-comment-501",
      ThreadID: null,
      Resolvable: true,
      Resolved: false,
      diff_thread: {
        id: "501",
        provider_comment_id: "review-comment-501",
        path: "src/review.ts",
        side: "right",
        start_side: "right",
        start_line: 10,
        line: 11,
        new_line: 11,
        line_type: "context",
        diff_head_sha: "head",
        commit_sha: "head",
        body: "This can return directly.",
        author_login: "reviewer",
        resolved: false,
        can_resolve: true,
        created_at: "2026-05-01T12:01:00Z",
        updated_at: "2026-05-01T12:01:00Z",
      },
    },
  ];
}

function makeReviewSuggestionDiffStore() {
  const diff: DiffResult = {
    stale: false,
    whitespace_only_count: 0,
    files: [
      {
        path: "src/review.ts",
        old_path: "src/review.ts",
        status: "modified",
        is_binary: false,
        is_whitespace_only: false,
        additions: 2,
        deletions: 0,
        hunks: [
          {
            old_start: 9,
            old_count: 1,
            new_start: 9,
            new_count: 3,
            lines: [
              {
                type: "context",
                old_num: 9,
                new_num: 9,
                content: "const client = setup();",
              },
              {
                type: "add",
                new_num: 10,
                content: "client.enableReviews();",
              },
              {
                type: "add",
                new_num: 11,
                content: "client.publishThreads();",
              },
              {
                type: "context",
                old_num: 10,
                new_num: 12,
                content: "return client;",
              },
            ],
          },
        ],
      },
    ],
  };
  return {
    getDiff: () => diff,
    isDiffLoading: () => false,
    getCurrentPR: () => ({
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
      number: 1,
    }),
    getTabWidth: () => 4,
    loadDiff: vi.fn(),
    requestScrollToLine: vi.fn(),
  };
}

function getActionMenuLabelsButton(): HTMLButtonElement {
  const button = document.querySelector<HTMLButtonElement>(".actions-menu-popover .btn--labels");
  if (button === null) {
    throw new Error("actions menu Labels button not found");
  }
  return button;
}

describe("PullDetail approvals", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    markdownMockState.pending = false;
    markdownMockState.pendingPromise = new Promise<string>(() => undefined);
    cleanup();
    vi.useRealTimers();
  });

  it("shows approval count and expands approver names", async () => {
    renderPullDetail(pullDetail());

    const trigger = screen.getByRole("button", {
      name: "APPROVED (2)",
    });
    await fireEvent.click(trigger);

    const popup = document.querySelector(".approval-popup");
    expect(popup?.textContent).toContain("alice");
    expect(popup?.textContent).toContain("bob");

    await fireEvent.mouseDown(document.body);

    expect(document.querySelector(".approval-popup")).toBeNull();
  });

  it("uses the standard syncing indicator without duplicate progress UI", () => {
    renderPullDetail(pullDetail(), undefined, undefined, {
      detailSyncing: true,
    });

    expect(document.querySelector(".sync-indicator")?.textContent).toContain("Syncing");
    expect(document.querySelector(".refresh-banner")).toBeNull();
    expect(screen.queryByText("Refreshing...")).toBeNull();
  });

  it("keeps task checkboxes disabled while highlighted markdown is pending", () => {
    markdownMockState.pending = true;
    const detail = pullDetail();
    detail.merge_request.Body = ["- [ ] pending task", "", "```toml", 'model_provider = "my-custom"', "```"].join("\n");

    const { container } = renderPullDetail(detail);

    const checkbox = container.querySelector<HTMLInputElement>(".markdown-body input[type='checkbox']");
    expect(checkbox).not.toBeNull();
    expect(checkbox?.disabled).toBe(true);
    expect(checkbox?.dataset.taskIndex).toBeUndefined();
  });

  it("explains that creating a workspace enables agent sessions", () => {
    renderPullDetail(pullDetail(), undefined, undefined, { hideWorkspaceAction: false });

    const buttons = screen.getAllByRole("button", { name: "Create Workspace" });
    expect(buttons.length).toBeGreaterThan(0);
    for (const button of buttons) {
      expect(button.getAttribute("title")).toContain("PR head worktree");
      expect(button.getAttribute("title")).toContain("launch agents");
      expect(button.getAttribute("title")).toContain("local review sessions");
      const descriptionId = button.getAttribute("aria-describedby");
      expect(descriptionId).toBeTruthy();
      expect(document.getElementById(descriptionId ?? "")?.textContent).toContain(button.getAttribute("title"));
    }
  });

  it("forwards worktree link host key to navigate actions", async () => {
    const detail = pullDetail();
    detail.worktree_links = [
      {
        host_key: "hub",
        worktree_key: "worktree:/srv/widget-feature",
        worktree_path: "/srv/widget-feature",
        worktree_branch: "feature",
      },
    ];
    const navigate = vi.fn();

    renderPullDetail(detail, undefined, undefined, {
      actions: {
        pull: [
          {
            id: "navigate-worktree",
            label: "Open Worktree",
            handler: navigate,
          },
        ],
      },
    });

    await fireEvent.click(
      screen.getByRole("button", {
        name: "Open Worktree: worktree:/srv/widget-feature",
      }),
    );

    expect(navigate).toHaveBeenCalledWith({
      surface: "pull-detail",
      owner: "acme",
      name: "widget",
      number: 1,
      meta: {
        host_key: "hub",
        worktree_key: "worktree:/srv/widget-feature",
      },
    });
  });

  it("normalizes backend review decision casing before enabling approver popup", async () => {
    const detail = pullDetail();
    detail.merge_request.ReviewDecision = "approved";

    renderPullDetail(detail);

    const trigger = screen.getByRole("button", {
      name: "APPROVED (2)",
    });
    await fireEvent.click(trigger);

    const popup = document.querySelector(".approval-popup");
    expect(popup?.textContent).toContain("alice");
    expect(popup?.textContent).toContain("bob");
  });

  it("auto-refreshes pending CI checks while the CI panel is expanded", async () => {
    vi.useFakeTimers();
    const detail = pullDetail();
    detail.merge_request.CIStatus = "pending";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      {
        name: "build",
        status: "in_progress",
        conclusion: "",
        url: "https://example.com/build",
        app: "GitHub Actions",
      },
    ]);

    const { detailStore } = renderPullDetail(detail);

    expect(detailStore.refreshPendingCI).not.toHaveBeenCalled();

    await fireEvent.click(
      screen.getByRole("button", {
        name: /CI:\s*1\s*pending\s*check/i,
      }),
    );

    expect(detailStore.refreshPendingCI).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(15_000);

    expect(detailStore.refreshPendingCI).toHaveBeenCalledTimes(2);
    expect(detailStore.refreshPendingCI).toHaveBeenCalledWith("acme", "widget", 1, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      workflowApprovalSync: true,
    });
  });

  it("uses one shared expanded slot for CI and stack status", async () => {
    const detail = pullDetail();
    detail.merge_request.Number = 2;
    detail.merge_request.MergeableState = "dirty";
    detail.stack = {
      stack_id: 1,
      stack_name: "session-recovery",
      position: 2,
      size: 3,
      health: "blocked",
      members: [
        {
          number: 1,
          title: "base schema",
          state: "open",
          ci_status: "failure",
          review_decision: "APPROVED",
          position: 1,
          is_draft: false,
          base_branch: "main",
          blocked_by: null,
        },
        {
          number: 2,
          title: "session storage",
          state: "open",
          ci_status: "pending",
          review_decision: "APPROVED",
          position: 2,
          is_draft: false,
          base_branch: "feat/base-schema",
          blocked_by: 1,
        },
        {
          number: 3,
          title: "UI polish",
          state: "open",
          ci_status: "success",
          review_decision: "",
          position: 3,
          is_draft: false,
          base_branch: "feat/session-storage",
          blocked_by: 1,
        },
      ],
    };
    detail.merge_request.CIStatus = "pending";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      {
        name: "frontend / vp check",
        status: "completed",
        conclusion: "failure",
        url: "https://example.com/frontend",
        app: "GitHub Actions",
      },
      {
        name: "e2e / chromium",
        status: "in_progress",
        conclusion: "",
        url: "https://example.com/e2e",
        app: "GitHub Actions",
      },
    ]);

    const apiClient = {
      GET: vi.fn(async () => {
        return {
          data: {
            AllowSquashMerge: false,
            AllowMergeCommit: false,
            AllowRebaseMerge: false,
            ViewerCanMerge: true,
          },
        };
      }),
    };

    renderPullDetail(detail, undefined, apiClient);

    await fireEvent.click(
      screen.getByRole("button", {
        name: /CI:\s*1 failed check,\s*1 pending check/i,
      }),
    );

    expect(screen.getByText("frontend / vp check")).toBeTruthy();

    await fireEvent.click(
      await screen.findByRole("button", {
        name: /Stacked: 2\/3, 1 downstack CI failure/i,
      }),
    );

    expect(screen.queryByText("frontend / vp check")).toBeNull();
    expect(screen.getByText("3 PRs · current 2/3 · downstack CI failure")).toBeTruthy();
    expect(document.querySelector(".stack-row--current .stack-dot--current")).toBeTruthy();
    expect(screen.getByText("blocked by #1")).toBeTruthy();

    const stackLinks = Array.from(document.querySelectorAll<HTMLButtonElement>(".stack-member-link")).map((button) =>
      button.textContent?.trim(),
    );
    expect(stackLinks).toEqual(["#3 UI polish", "#2 session storage", "#1 base schema"]);
    expect(document.querySelector(".stack-base-name")?.textContent?.trim()).toBe("main");

    await fireEvent.click(screen.getByTestId("merge-warnings-chip"));

    expect(screen.queryByText("3 PRs · current 2/3 · downstack CI failure")).toBeNull();
    expect(screen.getByText("This branch has conflicts that must be resolved before merging.")).toBeTruthy();
  });

  it("does not probe stack context for unstacked pull details", () => {
    const apiClient = {
      GET: vi.fn(async () => ({
        data: {
          AllowSquashMerge: false,
          AllowMergeCommit: false,
          AllowRebaseMerge: false,
          ViewerCanMerge: true,
        },
      })),
    };

    renderPullDetail(pullDetail(), undefined, apiClient);

    const paths = apiClient.GET.mock.calls.map(([path]) => String(path));
    expect(paths.some((path) => path.endsWith("/stack"))).toBe(false);
  });

  it("closes the label picker when the labels action is clicked twice", async () => {
    const detail = pullDetail();
    detail.repo.capabilities = {
      ...capabilities,
      read_labels: true,
      label_mutation: true,
    };

    renderPullDetail(detail);

    const labelsAction = screen.getByRole("button", { name: "Labels" });
    await fireEvent.click(labelsAction);

    expect(await screen.findByRole("dialog", { name: "Edit labels" })).toBeTruthy();

    await fireEvent.click(labelsAction);

    expect(screen.queryByRole("dialog", { name: "Edit labels" })).toBeNull();
  });

  it("closes the label picker when the actions menu Labels action is clicked after reopening the menu", async () => {
    const detail = pullDetail();
    detail.repo.capabilities = {
      ...capabilities,
      read_labels: true,
      label_mutation: true,
    };

    renderPullDetail(detail);

    const actionsTrigger = screen.getByRole("button", {
      name: "Actions",
    });
    await fireEvent.click(actionsTrigger);
    await fireEvent.click(getActionMenuLabelsButton());

    expect(await screen.findByRole("dialog", { name: "Edit labels" })).toBeTruthy();
    expect(document.querySelector(".actions-menu-popover")).toBeNull();

    await fireEvent.mouseDown(actionsTrigger);
    await fireEvent.click(actionsTrigger);
    expect(document.querySelector(".actions-menu-popover")).not.toBeNull();

    const labelsAction = getActionMenuLabelsButton();
    await fireEvent.mouseDown(labelsAction);
    await fireEvent.click(labelsAction);

    expect(screen.queryByRole("dialog", { name: "Edit labels" })).toBeNull();
    expect(document.querySelector(".actions-menu-popover")).toBeNull();
  });

  it("opens the actions-menu label picker as a non-modal popover", async () => {
    const detail = pullDetail();
    detail.repo.capabilities = {
      ...capabilities,
      read_labels: true,
      label_mutation: true,
    };

    renderPullDetail(detail);

    await fireEvent.click(screen.getByRole("button", { name: "Actions" }));
    await fireEvent.click(getActionMenuLabelsButton());

    expect(await screen.findByRole("dialog", { name: "Edit labels" })).toBeTruthy();
    expect(document.querySelector(".label-editor-backdrop")).toBeNull();

    await fireEvent.mouseDown(document.body);
    expect(screen.queryByRole("dialog", { name: "Edit labels" })).toBeNull();
  });

  it("keeps the actions menu Labels button on the compact action geometry", async () => {
    const detail = pullDetail();
    detail.repo.capabilities = {
      ...capabilities,
      read_labels: true,
      label_mutation: true,
    };

    renderPullDetail(detail);

    await fireEvent.click(screen.getByRole("button", { name: "Actions" }));

    const labelsAction = getActionMenuLabelsButton();
    const labelsIcon = labelsAction.querySelector("svg");
    const labelsItem = labelsAction.closest(".actions-menu-popover__item--labels");

    expect(labelsAction.classList.contains("kit-button--sm")).toBe(true);
    expect(labelsAction.parentElement).toBe(labelsItem);
    expect(labelsItem?.classList.contains("label-editor-anchor")).toBe(true);
    expect(labelsIcon?.getAttribute("width")).toBe("14");
    expect(labelsIcon?.getAttribute("height")).toBe("14");
  });

  it("uses the shared View menu to persist compact activity rows", async () => {
    const detail = pullDetail();
    detail.events = [
      {
        ID: 30,
        MergeRequestID: 1,
        PlatformID: 30,
        PlatformExternalID: "",
        EventType: "issue_comment",
        Author: "alice",
        Summary: "",
        Body: "Compact **activity** preview",
        MetadataJSON: "",
        CreatedAt: "2026-05-01T12:03:00Z",
        DedupeKey: "comment-30",
        DirectURL: "",
        ThreadID: null,
        Resolvable: false,
        Resolved: false,
      },
    ];
    const { container } = renderPullDetail(detail);

    expect(screen.queryByRole("button", { name: /filters/i })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: /view/i }));
    await fireEvent.click(screen.getByRole("button", { name: /compact/i }));

    expect(localStorage.getItem("middleman-detail-activity-view")).toBe("compact");
    expect(container.querySelectorAll(".event-card--compact-row")).toHaveLength(1);
    expect(container.textContent).toContain("Compact activity preview");
  });

  it("does not describe GitHub unstable mergeability as required checks", () => {
    const detail = pullDetail();
    detail.merge_request.MergeableState = "unstable";
    detail.merge_request.CIStatus = "failure";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      {
        name: "e2e",
        status: "completed",
        conclusion: "failure",
        url: "https://example.com/e2e",
        app: "GitHub Actions",
      },
    ]);

    renderPullDetail(detail);

    expect(screen.queryByTestId("merge-warnings-chip")).toBeNull();
  });

  it("shows required CI and branch freshness warnings behind the merge warnings chip", async () => {
    const detail = pullDetail();
    detail.merge_request.MergeableState = "behind";
    detail.merge_request.CIStatus = "failure";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      {
        name: "build",
        status: "completed",
        conclusion: "failure",
        url: "https://example.com/build",
        app: "GitHub Actions",
        required: true,
      },
    ]);

    renderPullDetail(detail);

    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("2 merge warnings");
    expect(screen.queryByText("Required status checks have not passed.")).toBeNull();

    await fireEvent.click(chip);

    expect(screen.getByText("Required status checks have not passed.")).toBeTruthy();
    expect(screen.getByText("This branch is behind the base branch and may need to be updated.")).toBeTruthy();
  });

  it("labels the chip Conflicts and links to the provider when the branch is dirty", async () => {
    const detail = pullDetail();
    detail.merge_request.MergeableState = "dirty";

    renderPullDetail(detail);

    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("Conflicts");

    await fireEvent.click(chip);

    expect(screen.getByText("This branch has conflicts that must be resolved before merging.")).toBeTruthy();
    const link = screen.getByRole("link", { name: "View on GitHub" });
    expect(link.getAttribute("href")).toBe(detail.merge_request.URL);
  });

  it("surfaces branch protection warnings through the merge warnings chip", async () => {
    const detail = pullDetail();
    detail.merge_request.MergeableState = "blocked";

    renderPullDetail(detail);

    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("1 merge warning");

    await fireEvent.click(chip);

    expect(screen.getByText("Branch protection rules may prevent this merge.")).toBeTruthy();
  });

  it("shows only server warnings on the chip when the detail is stale", async () => {
    const detail = pullDetail();
    detail.repo_owner = "someone-else";
    detail.merge_request.MergeableState = "dirty";
    detail.warnings = ["Example sync warning"];

    renderPullDetail(detail);

    const chip = screen.getByTestId("merge-warnings-chip");
    expect(chip.textContent).toContain("1 merge warning");

    await fireEvent.click(chip);

    expect(screen.getByText("Example sync warning")).toBeTruthy();
    expect(screen.queryByText("This branch has conflicts that must be resolved before merging.")).toBeNull();
  });

  it("does not render the merge button when repo permissions disallow merging", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.merge_mutation = true;

    renderPullDetail(detail, {
      AllowSquashMerge: true,
      AllowMergeCommit: true,
      AllowRebaseMerge: true,
      ViewerCanMerge: false,
    });

    await vi.waitFor(() => {
      expect(screen.queryByRole("button", { name: /merge/i })).toBeNull();
    });
  });

  it("renders the merge button as disabled with reason when operations.merge_pr.available is false", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.merge_mutation = true;
    const retryAt = "2026-05-19T14:35:00Z";
    const localRetryTime = new Date(retryAt).toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    });

    renderPullDetail(detail, {
      AllowSquashMerge: true,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      ViewerCanMerge: true,
      operations: {
        merge_pr: {
          available: false,
          code: "rate_limited",
          unavailable_reason: "github.com rate-limited",
          retry_at: retryAt,
        },
      },
    });

    const button = await vi.waitFor(() => {
      const found = screen.queryByRole("button", { name: /merge/i });
      expect(found).not.toBeNull();
      return found as HTMLButtonElement;
    });
    expect(button.disabled).toBe(true);
    expect(button.title).toBe(`github.com rate-limited; retry at ${localRetryTime}`);
  });

  it("disables ready-for-review with reason when its operation is unavailable", async () => {
    // A GitHub App split host with no user write credential reports
    // missing_write_credential on every mutation; non-merge actions
    // must disable with the reason instead of failing at request time.
    const detail = pullDetail();
    detail.repo.capabilities.ready_for_review = true;
    detail.merge_request.IsDraft = true;

    renderPullDetail(detail, {
      AllowSquashMerge: true,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      ViewerCanMerge: true,
      operations: {
        mark_ready_for_review: {
          available: false,
          code: "missing_write_credential",
          unavailable_reason: "No user credential for writes on github.com",
        },
      },
    });

    const button = await vi.waitFor(() => {
      const found = screen.queryByRole("button", { name: /ready for review/i });
      expect(found).not.toBeNull();
      return found as HTMLButtonElement;
    });
    expect(button.disabled).toBe(true);
    expect(button.title).toBe("No user credential for writes on github.com");
  });

  it("opens a state menu from the open chip and marks a pull request as draft", async () => {
    const detail = pullDetail();
    detail.repo.capabilities = {
      ...capabilities,
      draft_mutation: true,
    } as PullDetail["repo"]["capabilities"];
    const apiClient = {
      GET: vi.fn(async () => ({
        data: {
          AllowSquashMerge: false,
          AllowMergeCommit: false,
          AllowRebaseMerge: false,
          ViewerCanMerge: true,
        },
      })),
      POST: vi.fn(async () => ({ data: { state: "draft" } })),
    };

    const { detailStore } = renderPullDetail(detail, undefined, apiClient);

    await fireEvent.click(screen.getByRole("button", { name: "State: Open" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Draft" }));

    expect(apiClient.POST).toHaveBeenCalledWith("/pulls/{provider}/{owner}/{name}/{number}/github-state", {
      params: { path: { provider: "github", owner: "acme", name: "widget", number: 1 } },
      body: { state: "draft" },
    });
    expect(detailStore.loadDetail).toHaveBeenCalledWith("acme", "widget", 1, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    });
  });

  it("gates actions from the detail payload before repo settings resolve", async () => {
    // The detail payload carries repo.operations as the primary
    // source; the separate /repo settings request is only a fallback.
    // Here the settings response has no operations at all, so only a
    // payload-sourced gate can disable the button.
    const detail = pullDetail();
    detail.repo.capabilities.ready_for_review = true;
    detail.merge_request.IsDraft = true;
    detail.repo.operations = {
      mark_ready_for_review: {
        available: false,
        code: "missing_write_credential",
        unavailable_reason: "No user credential for writes on github.com",
      },
    } as unknown as PullDetail["repo"]["operations"];

    renderPullDetail(detail);

    const button = await vi.waitFor(() => {
      const found = screen.queryByRole("button", { name: /ready for review/i });
      expect(found).not.toBeNull();
      return found as HTMLButtonElement;
    });
    expect(button.disabled).toBe(true);
    expect(button.title).toBe("No user credential for writes on github.com");
  });

  it("hides review suggestion apply actions when the pull request is not open", async () => {
    const detail = pullDetail();
    addReviewSuggestionToDetail(detail);
    detail.merge_request.State = "closed";

    renderPullDetail(detail);

    expect(screen.getByText("This can return directly.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Commit suggestion" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add suggestion to batch" })).toBeNull();
  });

  it("routes a failed suggestion refresh through shared conflict recovery", async () => {
    const detail = pullDetail();
    addReviewSuggestionToDetail(detail);
    const repoSettings = {
      AllowSquashMerge: false,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      ViewerCanMerge: true,
      operations: detail.repo.operations,
    };
    const apiClient = {
      GET: vi.fn(async (path: string) => {
        if (path.includes("/repos/")) {
          return { data: repoSettings };
        }
        return { data: detail };
      }),
      POST: vi.fn(async (path: string) => {
        if (path.endsWith("/review-suggestions/apply")) {
          return {
            error: {
              code: "conflict",
              type: "about:blank",
              detail: "target changed since it was reviewed; refresh and retry",
              details: { reason: "stale_state" },
            },
          };
        }
        if (path.endsWith("/sync")) {
          return { error: undefined };
        }
        return { data: {} };
      }),
    };
    const detailStore = createDetailStore({
      client: apiClient as unknown as MiddlemanClient,
    });
    await detailStore.loadDetail("acme", "widget", 1, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false,
    });

    render(PullDetailComponent, {
      props: {
        owner: "acme",
        name: "widget",
        number: 1,
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
        hideWorkspaceAction: true,
        // Background sync would re-fetch the original fixture and race
        // the fail-closed state this test asserts.
        autoSync: false,
      },
      context: new Map<symbol, unknown>([
        [API_CLIENT_KEY, apiClient],
        [
          STORES_KEY,
          {
            detail: detailStore,
            pulls: { loadPulls: vi.fn() },
            activity: { loadActivity: vi.fn() },
            diff: makeReviewSuggestionDiffStore(),
            diffReviewDraft: {
              setRouteContext: vi.fn(),
              isSubmitting: () => false,
            },
            detailActivityView: createDetailActivityViewStore(),
          },
        ],
        [ACTIONS_KEY, { pull: [] }],
        [UI_CONFIG_KEY, { hideStar: true }],
        [NAVIGATE_KEY, vi.fn()],
      ]),
    });

    const commitButton = await vi.waitFor(() => {
      const found = screen.getByRole("button", { name: "Commit suggestion" }) as HTMLButtonElement;
      expect(found.disabled).toBe(false);
      return found;
    });
    await fireEvent.click(commitButton);

    await vi.waitFor(() => {
      expect(screen.queryByRole("button", { name: "Commit suggestion" })).toBeNull();
      expect(screen.queryByRole("button", { name: "Add suggestion to batch" })).toBeNull();
      expect(screen.getByText(/head commit changed since this pull request was reviewed/i)).toBeTruthy();
      expect(screen.getByRole("alert").textContent).toContain("Could not refresh the pull request");
    });
  });

  it("disables approve with reason when submit_review is unavailable", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.review_mutation = true;

    renderPullDetail(detail, {
      AllowSquashMerge: true,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      ViewerCanMerge: true,
      operations: {
        submit_review: {
          available: false,
          code: "missing_write_credential",
          unavailable_reason: "No user credential for writes on github.com",
        },
      },
    });

    const button = await vi.waitFor(() => {
      const found = screen.queryByRole("button", { name: /^approve$/i });
      expect(found).not.toBeNull();
      return found as HTMLButtonElement;
    });
    expect(button.disabled).toBe(true);
    expect(button.title).toBe("No user credential for writes on github.com");
  });

  it("opens the merge modal in deferred mode when CI is still pending", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.merge_mutation = true;
    detail.merge_request.CIStatus = "pending";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      {
        name: "build",
        status: "in_progress",
        conclusion: "",
        url: "https://example.com/build",
        app: "GitHub Actions",
      },
    ]);

    renderPullDetail(detail, {
      AllowSquashMerge: true,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      ViewerCanMerge: true,
    });

    await fireEvent.click(await screen.findByRole("button", { name: "Squash and merge" }));

    expect(screen.getByRole("dialog", { name: "Merge Pull Request" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Merge after CI is complete" })).toBeTruthy();
  });

  it("opens the merge modal in deferred mode when aggregate CI is pending without check rows", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.merge_mutation = true;
    detail.merge_request.CIStatus = "pending";
    detail.merge_request.CIChecksJSON = "";

    renderPullDetail(detail, {
      AllowSquashMerge: true,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      ViewerCanMerge: true,
    });

    await fireEvent.click(await screen.findByRole("button", { name: "Squash and merge" }));

    expect(screen.getByRole("dialog", { name: "Merge Pull Request" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Merge after CI is complete" })).toBeTruthy();
  });

  it("shows the queued merge state and still allows an immediate merge while a deferred merge waits", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.merge_mutation = true;
    detail.deferred_merge_pending = true;
    detail.merge_request.CIStatus = "pending";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      {
        name: "build",
        status: "in_progress",
        conclusion: "",
        url: "https://example.com/build",
        app: "GitHub Actions",
      },
    ]);

    renderPullDetail(detail, {
      AllowSquashMerge: true,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      ViewerCanMerge: true,
    });

    const queued = await vi.waitFor(() => {
      const found = screen.queryByRole("button", { name: "Merge queued" });
      expect(found).not.toBeNull();
      return found as HTMLButtonElement;
    });
    expect(queued.disabled).toBe(false);
    expect(queued.title).toContain("Click to merge immediately");

    // Clicking the queued action reopens the merge modal so the user can
    // force an immediate merge — but it must not offer queueing a second
    // deferred merge, which the server would reject.
    await fireEvent.click(queued);
    expect(screen.getByRole("dialog", { name: "Merge Pull Request" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Merge after CI is complete" })).toBeNull();
    expect(screen.getByRole("button", { name: "Squash and merge" })).toBeTruthy();
  });

  it("opens the merge modal in normal mode when aggregate CI has already failed", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.merge_mutation = true;
    detail.merge_request.CIStatus = "failure";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      {
        name: "build",
        status: "completed",
        conclusion: "failure",
        url: "https://example.com/build",
        app: "GitHub Actions",
      },
      {
        name: "integration",
        status: "in_progress",
        conclusion: "",
        url: "https://example.com/integration",
        app: "GitHub Actions",
      },
    ]);

    renderPullDetail(detail, {
      AllowSquashMerge: true,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      ViewerCanMerge: true,
    });

    await fireEvent.click(await screen.findByRole("button", { name: "Squash and merge" }));

    expect(screen.getByRole("dialog", { name: "Merge Pull Request" })).toBeTruthy();
    // A failed aggregate with a still-running check must not route to deferred
    // merge, since the backend would reject that with a 409.
    expect(screen.queryByRole("button", { name: "Merge after CI is complete" })).toBeNull();
  });

  it("keeps a newer head conflict after an overlapping approval succeeds", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.merge_mutation = true;
    detail.repo.capabilities.review_mutation = true;
    let resolveApprove!: (value: unknown) => void;
    const approveRequest = new Promise((resolve) => {
      resolveApprove = resolve;
    });
    let resolveMerge!: (value: unknown) => void;
    const mergeRequest = new Promise((resolve) => {
      resolveMerge = resolve;
    });
    const apiClient = {
      GET: vi.fn(async () => ({
        data: {
          AllowSquashMerge: true,
          AllowMergeCommit: false,
          AllowRebaseMerge: false,
          ViewerCanMerge: true,
        },
      })),
      POST: vi.fn((path: string) => {
        if (path.endsWith("/approve")) return approveRequest;
        if (path.endsWith("/merge")) return mergeRequest;
        return Promise.resolve({ data: {} });
      }),
    };
    const { detailStore } = renderPullDetail(
      detail,
      {
        AllowSquashMerge: true,
        AllowMergeCommit: false,
        AllowRebaseMerge: false,
        ViewerCanMerge: true,
      },
      apiClient,
    );

    await fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await fireEvent.click(
      within(screen.getByRole("dialog", { name: "Submit pull request review" })).getByRole("button", {
        name: "Approve",
      }),
    );
    await vi.waitFor(() => {
      expect(apiClient.POST.mock.calls.some(([path]) => String(path).endsWith("/approve"))).toBe(true);
    });
    await fireEvent.click(await screen.findByRole("button", { name: "Squash and merge" }));
    await fireEvent.click(
      within(screen.getByRole("dialog", { name: "Merge Pull Request" })).getByRole("button", {
        name: "Squash and merge",
      }),
    );

    resolveMerge({
      error: {
        code: "conflict",
        type: "about:blank",
        status: 409,
        detail: "head changed",
        details: { reason: "stale_state" },
      },
    });
    await vi.waitFor(() => {
      expect(screen.getByText(/head commit changed since this pull request was reviewed/i)).toBeTruthy();
    });

    const detailLoadsBeforeApproval = detailStore.loadDetail.mock.calls.length;
    resolveApprove({
      data: { status: "approved" },
      error: undefined,
      response: new Response("{}"),
    });
    await vi.waitFor(() => {
      expect(detailStore.loadDetail.mock.calls.length).toBeGreaterThan(detailLoadsBeforeApproval);
    });

    expect(screen.getByText(/head commit changed since this pull request was reviewed/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Approve" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("ignores a delayed merge conflict after an A-to-B-to-A route cycle", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.merge_mutation = true;
    detail.repo.capabilities.review_mutation = true;
    let resolveMerge: ((value: unknown) => void) | undefined;
    const apiClient = {
      GET: vi.fn(async () => ({
        data: {
          AllowSquashMerge: true,
          AllowMergeCommit: false,
          AllowRebaseMerge: false,
          ViewerCanMerge: true,
        },
      })),
      POST: vi.fn(
        () =>
          new Promise((resolve) => {
            resolveMerge = resolve;
          }),
      ),
    };
    const { rerender, detailStore } = renderPullDetail(
      detail,
      {
        AllowSquashMerge: true,
        AllowMergeCommit: false,
        AllowRebaseMerge: false,
        ViewerCanMerge: true,
      },
      apiClient,
    );

    await fireEvent.click(await screen.findByRole("button", { name: "Squash and merge" }));
    await fireEvent.click(
      within(screen.getByRole("dialog", { name: "Merge Pull Request" })).getByRole("button", {
        name: "Squash and merge",
      }),
    );
    expect((screen.getByRole("button", { name: "Approve" }) as HTMLButtonElement).disabled).toBe(false);
    await rerender({
      owner: "acme",
      name: "other-widget",
      number: 2,
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      repoPath: "acme/other-widget",
      hideWorkspaceAction: true,
    });
    await rerender({
      owner: "acme",
      name: "widget",
      number: 1,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      hideWorkspaceAction: true,
    });
    resolveMerge?.({
      error: {
        status: 409,
        detail: "head changed",
        details: { reason: "stale_state" },
      },
    });

    await vi.waitFor(() => expect(apiClient.POST).toHaveBeenCalledTimes(1));
    expect(detailStore.syncDetailNow).not.toHaveBeenCalled();
    expect(screen.queryByText(/head commit changed since/i)).toBeNull();
  });
});

describe("PullDetail inline workspace handoff", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    for (const item of getFlashes()) dismissFlash(item.id);
  });

  const identity: WorkspaceItemIdentity = {
    provider: "github",
    platformHost: "github.com",
    owner: "acme",
    name: "widget",
    repoPath: "acme/widget",
    number: 1,
    itemType: "pull",
  };

  function deferredWorkspaceApiClient() {
    let resolvePost!: (value: { data?: { id: string; status: string } }) => void;
    const postPromise = new Promise<{ data?: { id: string; status: string } }>((resolve) => {
      resolvePost = resolve;
    });
    const apiClient = {
      GET: vi.fn(async () => ({ data: {} })),
      POST: vi.fn(async () => postPromise),
    };
    return { apiClient, resolvePost };
  }

  it("create with inline controller records the override and does not navigate", async () => {
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { detailStore, navigate } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });

    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
    expect(navigate).not.toHaveBeenCalled();
    await vi.waitFor(() => {
      expect(detailStore.refreshDetailOnly).toHaveBeenCalledWith("acme", "widget", 1, {
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
      });
    });
  });

  it("records the override when a layout change unmounts the detail mid-create", async () => {
    // Tab switches, split-view toggles, and breakpoint crossings unmount
    // PullDetail with the same PR still selected; the successful response
    // must still land its store-level override instead of orphaning the
    // created workspace.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { detailStore, navigate, unmount } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);
    unmount();
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });

    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
    expect(navigate).not.toHaveBeenCalled();
    // No refetch from a destroyed component: its frozen identity cannot
    // see that the shared detail store may belong to a new selection.
    expect(detailStore.refreshDetailOnly).not.toHaveBeenCalled();
  });

  it("an alias-only route re-expression does not discard an in-flight create", async () => {
    // gh vs github and omitted vs concrete default host describe the same
    // PR; such a prop change mid-create must not bump the request
    // generation, or the success would be discarded and the button
    // re-enabled for a duplicate request.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { rerender } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);
    await rerender({ provider: "gh", platformHost: undefined });
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });

    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
  });

  it("keeps mutation actions live when the identity omits the host or aliases the provider", async () => {
    // Activity URLs may omit platform_host and route segments may carry
    // gh/gl aliases while the payload is canonical and concrete; the
    // stale guard must not block mutations on a detail that is current.
    const controller = createTestController("split");
    const { apiClient } = deferredWorkspaceApiClient();
    const { rerender } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await rerender({ provider: "gh", platformHost: undefined });
    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);

    expect(apiClient.POST).toHaveBeenCalled();
  });

  it("publishes a confirmed creation even after the selection changed", async () => {
    // The workspace exists server-side the moment the response confirms
    // it. Discarding it because the selection moved on would leave the
    // next visit to this PR offering "Create Workspace" again — a
    // duplicate submission. Only presentation (refetch, navigation)
    // stays tied to the live selection.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { detailStore, navigate, rerender } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);
    // Navigate to a different PR while the create request is in flight.
    await rerender({ number: 2 });
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    expect(detailStore.refreshDetailOnly).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
  });

  it("keeps Create Workspace disabled across a selection round-trip while a create is pending", async () => {
    // The local creating flag is cleared by the route-reset effect on
    // A→B and again on B→A; only the shared identity-keyed pending
    // store can keep the button disabled, or the second click sends a
    // duplicate create and earns a misleading "already exists" conflict.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { rerender } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);
    await rerender({ number: 2 });
    await rerender({ number: 1 });

    const button = screen.getAllByRole("button", { name: "Creating..." })[0]!;
    expect(button.hasAttribute("disabled")).toBe(true);
    await fireEvent.click(button);
    expect(apiClient.POST).toHaveBeenCalledTimes(1);

    resolvePost({ data: { id: "ws-new", status: "provisioning" } });
    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
    expect(controller.recordCreated).toHaveBeenCalledTimes(1);
  });

  it("keeps Create Workspace disabled across a remount while a create is pending", async () => {
    const controller = createTestController("split");
    const { apiClient } = deferredWorkspaceApiClient();
    renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);
    cleanup();

    const second = deferredWorkspaceApiClient();
    renderPullDetail(pullDetail(), undefined, second.apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: createTestController("split"),
    });

    const button = screen.getAllByRole("button", { name: "Creating..." })[0]!;
    expect(button.hasAttribute("disabled")).toBe(true);
    await fireEvent.click(button);
    expect(second.apiClient.POST).not.toHaveBeenCalled();
  });

  it("publishes a confirmed creation across a selection round-trip", async () => {
    // A→B→A: returning to the original PR restores an identity that
    // matches the request, but the round-trip bumped the request
    // generation. The confirmed creation must still land its override,
    // or the re-rendered detail shows "Create Workspace" until an
    // unrelated refetch.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { rerender } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);
    await rerender({ number: 2 });
    await rerender({ number: 1 });
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });

    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
  });

  it("discards a create failure that lands after the selection changed (no flash)", async () => {
    const controller = createTestController("split");
    let resolvePost!: (value: { error?: { detail?: string } }) => void;
    const postPromise = new Promise<{ error?: { detail?: string } }>((resolve) => {
      resolvePost = resolve;
    });
    const apiClient = {
      GET: vi.fn(async () => ({ data: {} })),
      POST: vi.fn(async () => postPromise),
    };
    const { rerender } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);
    // Navigate to a different PR while the create request is in flight.
    await rerender({ number: 2 });
    resolvePost({ error: { detail: "boom" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    // A failure toast about a PR the user already left is noise: without
    // the identity guard this would show "boom".
    expect(getFlashes()).toHaveLength(0);
  });

  it("without a controller create navigates to the terminal (today's behavior)", async () => {
    const apiClient = {
      GET: vi.fn(async () => ({ data: {} })),
      POST: vi.fn(async () => ({ data: { id: "ws-new", status: "provisioning" } })),
    };
    const { navigate } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);

    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith("/terminal/ws-new"));
  });

  it("a late create response cannot navigate after unmount (no controller)", async () => {
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { navigate } = renderPullDetail(pullDetail(), undefined, apiClient, {
      hideWorkspaceAction: false,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Create Workspace" })[0]!);
    cleanup();
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(navigate).not.toHaveBeenCalled();
  });

  it("open action becomes focus-terminal with a secondary open-in-workspaces", async () => {
    const detail = pullDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");
    // No override recorded: the controller passes the envelope ref through.
    controller.effectiveWorkspaceRef = vi.fn((_identity, envelopeRef) => envelopeRef ?? null);

    const { navigate } = renderPullDetail(detail, undefined, undefined, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    await fireEvent.click(screen.getAllByRole("button", { name: "Focus Terminal" })[0]!);
    expect(controller.focusTerminal).toHaveBeenCalled();

    await fireEvent.click(screen.getAllByRole("button", { name: "Open in Workspaces" })[0]!);
    expect(controller.openInWorkspaces).toHaveBeenCalledWith({ id: "ws-1", status: "ready" });
    expect(navigate).not.toHaveBeenCalled();
  });

  it("without a controller open renders a single Open Workspace button that navigates", async () => {
    const detail = pullDetail();
    detail.workspace = { id: "ws-1", status: "ready" };

    const { navigate } = renderPullDetail(detail, undefined, undefined, {
      hideWorkspaceAction: false,
    });

    expect(screen.queryByRole("button", { name: "Focus Terminal" })).toBeNull();
    await fireEvent.click(screen.getAllByRole("button", { name: "Open Workspace" })[0]!);
    expect(navigate).toHaveBeenCalledWith("/terminal/ws-1");
  });

  it("consults the override layer for button state", () => {
    const detail = pullDetail();
    detail.workspace = undefined;
    const controller = createTestController("split");
    controller.effectiveWorkspaceRef = vi.fn(() => ({ id: "ws-o", status: "ready" }));

    renderPullDetail(detail, undefined, undefined, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    expect(screen.queryByRole("button", { name: "Create Workspace" })).toBeNull();
    expect(screen.getAllByRole("button", { name: "Focus Terminal" }).length).toBeGreaterThan(0);
  });

  it("consults the override layer for button state (tombstone hides an envelope workspace)", () => {
    const detail = pullDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");
    controller.effectiveWorkspaceRef = vi.fn(() => null);

    renderPullDetail(detail, undefined, undefined, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    expect(screen.queryByRole("button", { name: "Focus Terminal" })).toBeNull();
    expect(screen.getAllByRole("button", { name: "Create Workspace" }).length).toBeGreaterThan(0);
  });

  it("reconciles the override on identity-matched detail load", () => {
    const detail = pullDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");

    renderPullDetail(detail, undefined, undefined, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });

    expect(controller.reconcile).toHaveBeenCalledWith(identity, { id: "ws-1", status: "ready" });
  });

  it("reconciles when the identity omits the host and the detail carries the provider default", async () => {
    // Activity URLs may omit platform_host; the loaded detail always
    // carries the concrete default host. The identity-match guard must
    // treat them as one item or the override never reconciles.
    const detail = pullDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");

    const { rerender } = renderPullDetail(detail, undefined, undefined, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });
    controller.reconcile.mockClear();

    await rerender({ platformHost: undefined });

    expect(controller.reconcile).toHaveBeenCalledWith(
      { ...identity, platformHost: undefined },
      { id: "ws-1", status: "ready" },
    );
  });

  it("does not reconcile the override for a detail belonging to a different identity", async () => {
    const detail = pullDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");

    const { rerender } = renderPullDetail(detail, undefined, undefined, {
      hideWorkspaceAction: false,
      inlineWorkspace: controller,
    });
    controller.reconcile.mockClear();

    // Same detail object (still describes PR #1), but props move to a
    // different number: detailMatchesIdentity must now return false so a
    // load for the stale identity can't reconcile an override recorded for
    // the newly-selected PR. Without the guard this would call reconcile
    // with the mismatched identity.
    await rerender({ number: 999 });

    expect(controller.reconcile).not.toHaveBeenCalled();
  });

  it("restores split before opening the label picker while the dock is expanded", async () => {
    // Expanded dock hides this detail (mounted, hidden+inert) but its
    // window-level command listener stays live; without the split reset the
    // picker would open invisibly and pop up on the next collapse.
    const controller = { ...createTestController("expanded"), isClaimedFor: () => true };
    const detail = pullDetail();
    detail.repo.capabilities = { ...capabilities, read_labels: true, label_mutation: true };
    renderPullDetail(detail, undefined, undefined, { inlineWorkspace: controller });

    openLabelPickerFor({
      itemType: "pull",
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
      number: 1,
    });

    expect(await screen.findByRole("dialog", { name: "Edit labels" })).toBeTruthy();
    expect(controller.setDockMode).toHaveBeenCalledWith("split");
  });

  it("label picker command leaves the dock mode alone when this detail is not claimed", async () => {
    // getDockMode() can read "expanded" from a surface whose claim belongs
    // to nothing (panel inactive -> detail fully visible): the command must
    // not collapse a dock it is not hidden behind.
    const controller = createTestController("expanded");
    const detail = pullDetail();
    detail.repo.capabilities = { ...capabilities, read_labels: true, label_mutation: true };
    renderPullDetail(detail, undefined, undefined, { inlineWorkspace: controller });

    openLabelPickerFor({
      itemType: "pull",
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
      number: 1,
    });

    expect(await screen.findByRole("dialog", { name: "Edit labels" })).toBeTruthy();
    expect(controller.setDockMode).not.toHaveBeenCalled();
  });
});

describe("PullDetail body copy feedback", () => {
  beforeEach(() => {
    localStorage.clear();
    clipboardMockState.resolvers.length = 0;
  });

  afterEach(() => {
    cleanup();
  });

  function copyButton(): HTMLButtonElement {
    const button = document.querySelector<HTMLButtonElement>(".kit-copy-btn.body-copy");
    if (button === null) {
      throw new Error("body copy button not found");
    }
    return button;
  }

  it("shows copied feedback when the clipboard write resolves on the same pull", async () => {
    const detail = pullDetail();
    detail.merge_request.Body = "body text";
    renderPullDetail(detail);

    await fireEvent.click(copyButton());
    expect(clipboardMockState.resolvers).toHaveLength(1);
    clipboardMockState.resolvers[0]!(true);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(document.querySelector(".body-copy--copied")).not.toBeNull();
  });

  it("drops a clipboard write that resolves after navigating to another pull", async () => {
    const detail = pullDetail();
    detail.merge_request.Body = "body text";
    const { rerender } = renderPullDetail(detail);

    await fireEvent.click(copyButton());
    expect(clipboardMockState.resolvers).toHaveLength(1);

    // Navigate to a different pull while the clipboard promise is pending.
    await rerender({ number: detail.merge_request.Number + 1 });

    clipboardMockState.resolvers[0]!(true);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(document.querySelector(".body-copy--copied")).toBeNull();
  });
});
