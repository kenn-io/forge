import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { PullDetail } from "../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, SIDEBAR_KEY, STORES_KEY, UI_CONFIG_KEY } from "../context.js";
import type { PullRequestRouteRef } from "../routes.js";
import { createDetailActivityViewStore } from "../stores/detail-activity-view.svelte.js";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
import { createClaimTestController } from "./viewWorkspaceTestDoubles.svelte.js";

// This spec mounts the real PullDetail (unlike PRListView.test.ts, which
// mocks it out) because the behavior under test — an unsaved title draft
// surviving a dock expand/collapse round trip — depends on PullDetail's own
// `$state` staying mounted, which a stand-in component cannot exercise.
import PRListView from "./PRListView.svelte";

const selectedPR: PullRequestRouteRef = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 12,
};

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
    platform_host: selectedPR.platformHost,
    repo_owner: selectedPR.owner,
    repo_name: selectedPR.name,
    warnings: [],
    workflow_approval: {
      count: 0,
      required: false,
      runs: [],
    },
    workspace: { id: "ws-1", status: "ready" },
    worktree_links: [],
    repo: {
      ID: 1,
      Owner: selectedPR.owner,
      Name: selectedPR.name,
      Host: selectedPR.platformHost ?? "",
      PlatformHost: selectedPR.platformHost,
      Platform: selectedPR.provider,
      URL: `https://github.com/${selectedPR.repoPath}`,
      DefaultBranch: "main",
      IsArchived: false,
      AllowSquashMerge: false,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      capabilities,
      provider: selectedPR.provider,
      platform_host: selectedPR.platformHost,
      owner: selectedPR.owner,
      name: selectedPR.name,
      repo_path: selectedPR.repoPath,
    },
    merge_request: {
      ID: 1,
      RepoID: 1,
      PlatformID: 100,
      PlatformExternalID: "PR_1",
      Number: selectedPR.number,
      URL: `https://github.com/${selectedPR.repoPath}/pull/${selectedPR.number}`,
      Title: "Make approval counts visible",
      Author: "octocat",
      AuthorDisplayName: "Octocat",
      State: "open",
      IsDraft: false,
      IsLocked: false,
      Body: "",
      HeadBranch: "feature",
      BaseBranch: "main",
      HeadRepoCloneURL: `https://github.com/${selectedPR.repoPath}.git`,
      Additions: 0,
      Deletions: 0,
      CommentCount: 0,
      ReviewDecision: "",
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
    events: [],
  };
}

function renderWithRealPullDetail(
  detail: PullDetail,
  inlineWorkspace: ReturnType<typeof createClaimTestController>["controller"],
) {
  const detailStore = {
    loadDetail: vi.fn(async () => undefined),
    startDetailPolling: vi.fn(),
    stopDetailPolling: vi.fn(),
    getDetail: () => detail,
    isDetailLoading: () => false,
    getDetailError: () => null,
    isDetailSyncing: () => false,
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
  const apiClient = {
    GET: vi.fn(async () => ({ data: {} })),
    POST: vi.fn(async () => ({ data: {} })),
  };

  return {
    detailStore,
    ...render(PRListView, {
      props: {
        selectedPR,
        detailTab: "conversation" as const,
        hideSidebar: true,
        inlineWorkspace,
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
        [
          SIDEBAR_KEY,
          {
            isSidebarToggleEnabled: () => false,
            toggleSidebar: vi.fn(),
          },
        ],
      ]),
    }),
  };
}

describe("PRListView workspace dock draft survival", () => {
  beforeEach(() => {
    localStorage.clear();
    resetModalStack();
    vi.stubGlobal(
      "MutationObserver",
      class {
        observe(): void {}
        disconnect(): void {}
        takeRecords(): MutationRecord[] {
          return [];
        }
      },
    );
    vi.stubGlobal("requestAnimationFrame", () => 1);
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
    localStorage.clear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("keeps an unsaved title draft mounted across an expand/collapse round trip", async () => {
    const { controller } = createClaimTestController();
    const { container } = renderWithRealPullDetail(pullDetail(), controller);
    await tick();

    expect(controller.claim).toHaveBeenCalled();

    const editTitleButton = container.querySelector<HTMLButtonElement>(".edit-title-btn");
    expect(editTitleButton).toBeTruthy();
    await fireEvent.click(editTitleButton!);
    const titleInput = container.querySelector<HTMLInputElement>(".title-edit-input");
    expect(titleInput).toBeTruthy();
    await fireEvent.input(titleInput!, { target: { value: "Draft title in progress" } });
    expect(titleInput!.value).toBe("Draft title in progress");

    controller.setDockMode("expanded");
    await tick();

    const detailWrapper = container.querySelector(".workspace-dock-detail");
    expect(detailWrapper?.hasAttribute("hidden")).toBe(true);
    const titleInputWhileExpanded = container.querySelector<HTMLInputElement>(".title-edit-input");
    expect(titleInputWhileExpanded).toBe(titleInput);
    expect(titleInputWhileExpanded!.value).toBe("Draft title in progress");

    controller.setDockMode("split");
    await tick();

    const titleInputAfterCollapse = container.querySelector<HTMLInputElement>(".title-edit-input");
    expect(titleInputAfterCollapse).toBe(titleInput);
    expect(titleInputAfterCollapse!.value).toBe("Draft title in progress");
  });
});
