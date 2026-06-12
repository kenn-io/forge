// Vitest component conversion of the GitLab read-only issue case from
// frontend/tests/e2e-full/provider-capabilities.spec.ts. Renders IssueDetail
// from the gitlab.example.com group/project#11 fixture captured from the
// seeded e2e Go server (capabilities: comment_mutation false, read_issues and
// read_comments true) and asserts the timeline hides edit controls while
// keeping the read-only copy affordance.
//
// The original spec also asserted the raw /api/v1 capabilities JSON returned
// by the Go server; that sub-assertion is dropped here — the backend contract
// is owned by the Go API tests, and this fixture was captured from that same
// seeded server response.
import { cleanup, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { IssueDetail as IssueDetailResponse } from "../../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, STORES_KEY, UI_CONFIG_KEY } from "../../context.js";
import issueGitlab11Json from "../../testing/e2e-fixtures/issue-gitlab-11.json";
import IssueDetailComponent from "./IssueDetail.svelte";

const issueGitlab11 = issueGitlab11Json as unknown as IssueDetailResponse;

function renderIssueDetail(detail: IssueDetailResponse) {
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
    editIssueComment: vi.fn(),
    toggleIssueStar: vi.fn(),
    loadIssues: vi.fn(),
    setIssueLabels: vi.fn(),
    setIssueAssignees: vi.fn(),
    saveIssueBodyInBackground: vi.fn(),
    setLocalIssueBody: vi.fn(),
  };
  return render(IssueDetailComponent, {
    props: {
      owner: "group",
      name: "project",
      number: 11,
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      repoPath: "group/project",
    },
    context: new Map<symbol, unknown>([
      [API_CLIENT_KEY, { GET: vi.fn(async () => ({ data: null })) }],
      [
        STORES_KEY,
        {
          issues: issuesStore,
          activity: { loadActivity: vi.fn() },
        },
      ],
      [ACTIONS_KEY, { issue: [] }],
      [UI_CONFIG_KEY, { hideStar: true }],
      [NAVIGATE_KEY, vi.fn()],
    ]),
  });
}

describe("IssueDetail read-only GitLab capabilities", () => {
  afterEach(() => {
    cleanup();
  });

  it("hides timeline edit controls and keeps copy when comments are read-only", () => {
    const detail = structuredClone(issueGitlab11);
    // Guard the fixture preconditions the UI behavior depends on.
    expect(detail.repo?.capabilities?.comment_mutation).toBe(false);
    expect(detail.repo?.capabilities?.read_issues).toBe(true);
    expect(detail.repo?.capabilities?.read_comments).toBe(true);

    renderIssueDetail(detail);

    const issueDetail = document.querySelector<HTMLElement>(".issue-detail");
    expect(issueDetail).not.toBeNull();
    const scope = within(issueDetail!);

    expect(scope.getByText("GitLab read-only issue")).toBeTruthy();
    expect(scope.getByText("GitLab issue body")).toBeTruthy();
    expect(scope.getByText("GitLab read-only timeline comment")).toBeTruthy();

    expect(scope.queryByRole("button", { name: "Edit comment" })).toBeNull();
    expect(scope.getByRole("button", { name: "Copy comment" })).toBeTruthy();
  });

  it("offers timeline edit controls when the provider allows comment mutation", () => {
    // Contrast case proving the read-only branch above is capability-driven,
    // not a hardcoded absence of the edit button.
    const detail = structuredClone(issueGitlab11);
    detail.repo!.capabilities!.comment_mutation = true;

    renderIssueDetail(detail);

    expect(screen.getByRole("button", { name: "Edit comment" })).toBeTruthy();
  });
});
