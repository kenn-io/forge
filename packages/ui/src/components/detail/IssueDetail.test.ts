import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { IssueDetail } from "../../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, STORES_KEY, UI_CONFIG_KEY } from "../../context.js";
import { createDetailActivityViewStore } from "../../stores/detail-activity-view.svelte.js";
import IssueDetailComponent from "./IssueDetail.svelte";

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
  draft_mutation: false,
  issue_mutation: true,
  label_mutation: false,
  assignee_mutation: false,
  reviewer_mutation: false,
  thread_reply: false,
  thread_resolve: false,
  review_draft_mutation: false,
  review_thread_resolution: false,
  read_review_threads: false,
  native_multiline_ranges: false,
  mutation_head_binding: false,
  supported_review_actions: [],
};

function issueDetail(): IssueDetail {
  return {
    detail_loaded: true,
    detail_fetched_at: "2026-05-01T12:05:00Z",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widget",
    workspace: undefined,
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
    issue: {
      ID: 1,
      RepoID: 1,
      PlatformID: 100,
      PlatformExternalID: "ISSUE_1",
      Number: 7,
      URL: "https://github.com/acme/widget/issues/7",
      Title: "Track compact issue activity",
      Author: "octocat",
      Body: "",
      State: "open",
      CommentCount: 1,
      CreatedAt: "2026-05-01T11:00:00Z",
      UpdatedAt: "2026-05-01T12:00:00Z",
      LastActivityAt: "2026-05-01T12:00:00Z",
      ClosedAt: null,
      DetailFetchedAt: "2026-05-01T12:05:00Z",
      Starred: false,
      labels: [],
      assignees: [],
      detail_loaded: true,
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widget",
        repo_path: "acme/widget",
      },
      repo_owner: "acme",
      repo_name: "widget",
      platform_host: "github.com",
    },
    events: [
      {
        ID: 20,
        IssueID: 1,
        PlatformID: 20,
        PlatformExternalID: "",
        EventType: "issue_comment",
        Author: "alice",
        Summary: "",
        Body: "Issue **activity** preview",
        MetadataJSON: "",
        CreatedAt: "2026-05-01T12:01:00Z",
        DedupeKey: "issue-comment-20",
        DirectURL: "",
        ThreadID: null,
      },
    ],
  };
}

function renderIssueDetail(detail: IssueDetail) {
  const issuesStore = {
    loadIssueDetail: vi.fn(async () => undefined),
    startIssueDetailPolling: vi.fn(),
    stopIssueDetailPolling: vi.fn(),
    getIssueDetail: () => detail,
    isIssueDetailLoading: () => false,
    getIssueDetailError: () => null,
    isIssueStaleRefreshing: () => false,
    isIssueDetailSyncing: () => false,
    getIssueDetailLoaded: () => true,
    loadIssues: vi.fn(async () => undefined),
    updateIssueKanbanState: vi.fn(),
    toggleIssueStar: vi.fn(),
    editIssueComment: vi.fn(),
    setIssueLabels: vi.fn(),
    setIssueAssignees: vi.fn(),
    saveIssueBodyInBackground: vi.fn(),
    setLocalIssueBody: vi.fn(),
  };

  return render(IssueDetailComponent, {
    props: {
      owner: "acme",
      name: "widget",
      number: detail.issue.Number,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    },
    context: new Map<symbol, unknown>([
      [API_CLIENT_KEY, { GET: vi.fn(), POST: vi.fn() }],
      [
        STORES_KEY,
        {
          issues: issuesStore,
          activity: { loadActivity: vi.fn() },
          detailActivityView: createDetailActivityViewStore(),
        },
      ],
      [ACTIONS_KEY, { issue: [] }],
      [UI_CONFIG_KEY, { hideStar: true }],
      [NAVIGATE_KEY, vi.fn()],
    ]),
  });
}

describe("IssueDetail activity view", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("offers compact activity rows from the shared View menu without PR filters", async () => {
    const { container } = renderIssueDetail(issueDetail());

    await fireEvent.click(screen.getByRole("button", { name: /view/i }));

    expect(screen.getByRole("button", { name: /normal/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /compact/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /messages/i })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: /compact/i }));

    expect(localStorage.getItem("middleman-detail-activity-view")).toBe("compact");
    expect(container.querySelectorAll(".event-card--compact-row")).toHaveLength(1);
    expect(container.textContent).toContain("Issue activity preview");
  });
});
