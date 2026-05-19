import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { PullDetail } from "../../api/types.js";
import {
  ACTIONS_KEY,
  API_CLIENT_KEY,
  NAVIGATE_KEY,
  STORES_KEY,
  UI_CONFIG_KEY,
} from "../../context.js";
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
    diff_head_sha: "head",
    merge_base_sha: "base",
    platform_base_sha: "base",
    platform_head_sha: "head",
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
) {
  const detailStore = {
    loadDetail: vi.fn(async () => undefined),
    startDetailPolling: vi.fn(),
    stopDetailPolling: vi.fn(),
    getDetail: () => detail,
    isDetailLoading: () => false,
    getDetailError: () => null,
    isStaleRefreshing: () => false,
    isDetailSyncing: () => false,
    getDetailLoaded: () => true,
    updateKanbanState: vi.fn(),
    toggleDetailPRStar: vi.fn(),
    updatePRContent: vi.fn(),
    refreshPendingCI: vi.fn(async () => undefined),
    editComment: vi.fn(),
  };

  const rendered = render(PullDetailComponent, {
    props: {
      owner: "acme",
      name: "widget",
      number: 1,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      hideWorkspaceAction: true,
    },
    context: new Map<symbol, unknown>([
      [
        API_CLIENT_KEY,
        {
          GET: vi.fn(async () => ({
            data: repoSettings,
          })),
        },
      ],
      [
        STORES_KEY,
        {
          detail: detailStore,
          pulls: { loadPulls: vi.fn() },
          activity: { loadActivity: vi.fn() },
        },
      ],
      [ACTIONS_KEY, { pull: [] }],
      [UI_CONFIG_KEY, { hideStar: true }],
      [NAVIGATE_KEY, vi.fn()],
    ]),
  });
  return { ...rendered, detailStore };
}

function getActionMenuLabelsButton(): HTMLButtonElement {
  const button = document.querySelector<HTMLButtonElement>(".actions-menu-popover .btn--labels");
  if (button === null) {
    throw new Error("actions menu Labels button not found");
  }
  return button;
}

describe("PullDetail approvals", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("shows approval count and expands approver names", async () => {
    renderPullDetail(pullDetail());

    const trigger = screen.getByRole("button", { name: "APPROVED (2)" });
    await fireEvent.click(trigger);

    const popup = document.querySelector(".approval-popup");
    expect(popup?.textContent).toContain("alice");
    expect(popup?.textContent).toContain("bob");

    await fireEvent.mouseDown(document.body);

    expect(document.querySelector(".approval-popup")).toBeNull();
  });

  it("normalizes backend review decision casing before enabling approver popup", async () => {
    const detail = pullDetail();
    detail.merge_request.ReviewDecision = "approved";

    renderPullDetail(detail);

    const trigger = screen.getByRole("button", { name: "APPROVED (2)" });
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
      screen.getByRole("button", { name: /CI:\s*pending \(1\)/i }),
    );

    expect(detailStore.refreshPendingCI).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(15_000);

    expect(detailStore.refreshPendingCI).toHaveBeenCalledTimes(2);
    expect(detailStore.refreshPendingCI).toHaveBeenCalledWith(
      "acme",
      "widget",
      1,
      {
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
        workflowApprovalSync: true,
      },
    );
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

    const actionsTrigger = screen.getByRole("button", { name: "Actions" });
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

    expect(labelsAction.classList.contains("action-button--sm")).toBe(true);
    expect(labelsAction.parentElement).toBe(labelsItem);
    expect(labelsItem?.classList.contains("label-editor-anchor")).toBe(true);
    expect(labelsIcon?.getAttribute("width")).toBe("14");
    expect(labelsIcon?.getAttribute("height")).toBe("14");
  });

  const warningCases = [
    {
      name: "does not describe GitHub unstable mergeability as required checks",
      mergeableState: "unstable",
      checks: [
        {
          name: "e2e",
          status: "completed",
          conclusion: "failure",
          url: "https://example.com/e2e",
          app: "GitHub Actions",
        },
      ],
      requiredWarning: false,
      behindWarning: false,
    },
    {
      name: "shows required CI and branch freshness warnings independently",
      mergeableState: "behind",
      checks: [
        {
          name: "build",
          status: "completed",
          conclusion: "failure",
          url: "https://example.com/build",
          app: "GitHub Actions",
          required: true,
        },
      ],
      requiredWarning: true,
      behindWarning: true,
    },
  ];

  for (const { name, mergeableState, checks, requiredWarning, behindWarning } of warningCases) {
    it(name, () => {
      const detail = pullDetail();
      detail.merge_request.MergeableState = mergeableState;
      detail.merge_request.CIStatus = "failure";
      detail.merge_request.CIChecksJSON = JSON.stringify(checks);

      renderPullDetail(detail);

      const requiredStatusWarning = screen.queryByText(
        "Required status checks have not passed.",
      );
      const behindBranchWarning = screen.queryByText(
        "This branch is behind the base branch and may need to be updated.",
      );
      if (requiredWarning) {
        expect(requiredStatusWarning).not.toBeNull();
      } else {
        expect(requiredStatusWarning).toBeNull();
      }
      if (behindWarning) {
        expect(behindBranchWarning).not.toBeNull();
      } else {
        expect(behindBranchWarning).toBeNull();
      }
    });
  }

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
      expect(
        screen.queryByRole("button", { name: /merge/i }),
      ).toBeNull();
    });
  });

  it("renders the merge button as disabled with reason when operations.merge_pr.available is false", async () => {
    const detail = pullDetail();
    detail.repo.capabilities.merge_mutation = true;

    renderPullDetail(detail, {
      AllowSquashMerge: true,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      ViewerCanMerge: true,
      operations: {
        merge_pr: {
          available: false,
          code: "rate_limited",
          unavailable_reason: "github.com rate-limited; retry at 14:35",
          retry_at: "2026-05-19T14:35:00Z",
        },
      },
    });

    const button = await vi.waitFor(() => {
      const found = screen.queryByRole("button", { name: /merge/i });
      expect(found).not.toBeNull();
      return found as HTMLButtonElement;
    });
    expect(button.disabled).toBe(true);
    expect(button.title).toBe("github.com rate-limited; retry at 14:35");
  });
});
