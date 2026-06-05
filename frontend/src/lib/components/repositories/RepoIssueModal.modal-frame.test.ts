import { cleanup, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import RepoIssueModal from "./RepoIssueModal.svelte";
import type { RepoSummaryCard } from "./repoSummary.js";
import { getStackDepth, getTopFrame, resetModalStack } from "@middleman/ui/stores/keyboard/modal-stack";

const summary: RepoSummaryCard = {
  owner: "acme",
  name: "widgets",
  platform_host: "github.com",
  repo: {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
  },
  cached_pr_count: 0,
  open_pr_count: 0,
  draft_pr_count: 0,
  cached_issue_count: 0,
  open_issue_count: 0,
  most_recent_activity_at: "2026-04-17T15:04:05Z",
  last_sync_completed_at: "",
  last_sync_started_at: "",
  last_sync_error: "",
  latest_release: undefined,
  releases: [],
  commits_since_release: 0,
  commit_timeline: [],
  active_authors: [],
  recent_issues: [],
} as unknown as RepoSummaryCard;

describe("RepoIssueModal modal frame integration", () => {
  beforeEach(() => {
    resetModalStack();
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
  });

  it("pushes a frame on mount and pops on unmount", () => {
    expect(getStackDepth()).toBe(0);
    const { unmount } = render(RepoIssueModal, {
      props: {
        summary,
        title: "",
        body: "",
        ontitlechange: vi.fn(),
        onbodychange: vi.fn(),
        oncancel: vi.fn(),
        onsubmitissue: vi.fn(),
      },
    });
    expect(getStackDepth()).toBe(1);
    expect(getTopFrame()?.frameId).toBe("repo-issue-modal");
    unmount();
    expect(getStackDepth()).toBe(0);
  });
});
