import { cleanup, render } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import RepoIssueModalTestHarness from "./RepoIssueModalTestHarness.svelte";
import type { RepoSummaryCard } from "./repoSummary.js";
import { getStackDepth, getTopFrame, resetModalStack } from "../../stores/keyboard/modal-stack.svelte.js";

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

let runtime: OwnedAppRuntime;

describe("RepoIssueModal modal frame integration", () => {
  beforeEach(() => {
    resetModalStack();
    runtime = makeAppRuntime();
  });

  afterEach(async () => {
    cleanup();
    resetModalStack();
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("pushes a frame on mount and pops on unmount", () => {
    expect(getStackDepth()).toBe(0);
    const { unmount } = render(RepoIssueModalTestHarness, {
      props: {
        runtime,
        modalProps: {
          summary,
          title: "",
          body: "",
          ontitlechange: vi.fn(),
          onbodychange: vi.fn(),
          oncancel: vi.fn(),
          onsubmitissue: vi.fn(),
        },
      },
    });
    expect(getStackDepth()).toBe(1);
    expect(getTopFrame()?.frameId).toBe("repo-issue-modal");
    unmount();
    expect(getStackDepth()).toBe(0);
  });
});
