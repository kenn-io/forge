import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { ProviderCapabilities, RepoOperations } from "../../api/types.js";

vi.mock("./DiffSidebar.svelte", async () => ({
  default: (await import("./DiffFilesLayoutTestSidebar.svelte")).default,
}));

vi.mock("./DiffToolbar.svelte", async () => ({
  default: (await import("./DiffFilesLayoutTestToolbar.svelte")).default,
}));

vi.mock("./DiffView.svelte", async () => ({
  default: (await import("./DiffFilesLayoutTestView.svelte")).default,
}));

import DiffFilesLayout from "./DiffFilesLayout.svelte";

const capabilities: ProviderCapabilities = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  read_labels: true,
  comment_mutation: true,
  state_mutation: true,
  merge_mutation: false,
  review_mutation: false,
  workflow_approval: false,
  ready_for_review: false,
  draft_mutation: false,
  issue_mutation: false,
  label_mutation: false,
  assignee_mutation: false,
  reviewer_mutation: false,
  thread_reply: true,
  thread_resolve: false,
  review_draft_mutation: true,
  review_thread_resolution: false,
  read_review_threads: true,
  native_multiline_ranges: false,
  supported_review_actions: ["COMMENT"],
};

function operation(available: boolean, reason = "") {
  return {
    available,
    unavailable_reason: reason,
  };
}

function operations(overrides: Partial<RepoOperations>): RepoOperations {
  return {
    merge_pr: operation(false),
    close_pr: operation(true),
    reopen_pr: operation(true),
    mark_ready_for_review: operation(false),
    submit_review: operation(false, "Provider does not support review_mutation"),
    review_draft: operation(true),
    add_comment: operation(true),
    edit_comment: operation(true),
    add_label: operation(true),
    remove_label: operation(true),
    set_assignees: operation(true),
    set_reviewers: operation(true),
    create_issue: operation(true),
    close_issue: operation(true),
    reopen_issue: operation(true),
    approve_workflow: operation(false),
    update_content: operation(true),
    reply_review_thread: operation(true),
    resolve_review_thread: operation(true),
    ...overrides,
  };
}

function renderLayout(repoOperations: RepoOperations): void {
  render(DiffFilesLayout, {
    props: {
      provider: "gitlab",
      platformHost: "gitlab.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
      number: 42,
      diffHeadSHA: "head",
      capabilities,
      operations: repoOperations,
    },
  });
}

describe("DiffFilesLayout operation gates", () => {
  afterEach(() => cleanup());

  it("keeps draft review authoring enabled when only submitted reviews are unsupported", () => {
    renderLayout(operations({}));

    expect(screen.getByTestId("review-draft-mutation").textContent).toBe("true");
    expect(screen.getByTestId("review-unavailable-reason").textContent).toBe("");
  });

  it("surfaces the review draft operation reason when draft authoring is unavailable", () => {
    renderLayout(
      operations({
        review_draft: operation(false, "No user credential for writes on gitlab.com"),
      }),
    );

    expect(screen.getByTestId("review-draft-mutation").textContent).toBe("false");
    expect(screen.getByTestId("review-unavailable-reason").textContent).toBe(
      "No user credential for writes on gitlab.com",
    );
  });
});
