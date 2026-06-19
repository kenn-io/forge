import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { PullDetail } from "../../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, STORES_KEY, UI_CONFIG_KEY } from "../../context.js";
import { createDetailActivityViewStore } from "../../stores/detail-activity-view.svelte.js";
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
      number: detail.merge_request.Number,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      hideWorkspaceAction: true,
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
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
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

      const requiredStatusWarning = screen.queryByText("Required status checks have not passed.");
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
      expect(screen.queryByRole("button", { name: /merge/i })).toBeNull();
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

    expect(screen.getByRole("heading", { name: "Merge Pull Request" })).toBeTruthy();
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

    expect(screen.getByRole("heading", { name: "Merge Pull Request" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Merge after CI is complete" })).toBeTruthy();
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

    expect(screen.getByRole("heading", { name: "Merge Pull Request" })).toBeTruthy();
    // A failed aggregate with a still-running check must not route to deferred
    // merge, since the backend would reject that with a 409.
    expect(screen.queryByRole("button", { name: "Merge after CI is complete" })).toBeNull();
  });
});
