import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { workspaceHoverDetails } from "./workspace-hover-details.js";
import type { WorkspaceListItem } from "./workspace-list-schema.js";

function workspace(overrides: Partial<WorkspaceListItem>): WorkspaceListItem {
  return {
    id: "ws-1",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    item_type: "pull_request",
    item_number: 42,
    source_item_visible: true,
    git_head_ref: "feature/auth",
    worktree_path: "/tmp/worktrees/ws-1",
    tmux_working: false,
    tmux_activity_source: "unknown",
    tmux_last_output_at: null,
    status: "ready",
    created_at: "2026-04-08T12:00:00Z",
    ...overrides,
  };
}

function labels(rows: readonly { label: string }[]): string[] {
  return rows.map((row) => row.label);
}

describe("workspaceHoverDetails", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-10T12:00:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("describes a pull request workspace and its worktree", () => {
    const details = workspaceHoverDetails(
      workspace({
        mr_state: "open",
        mr_is_draft: true,
        item_author: "alice",
        item_last_activity_at: "2026-04-10T09:00:00Z",
        mr_review_decision: "approved",
        mr_additions: 12,
        mr_deletions: 3,
        mr_comment_count: 2,
        agent_state: "input",
        agent_state_updated_at: "2026-04-10T11:55:00Z",
        tmux_pane_title: "claude: review auth",
        tmux_last_output_at: "2026-04-10T11:58:00Z",
        commits_ahead: 2,
        commits_behind: 1,
        commits_vs_pr_head: true,
        worktree_dirty: true,
      }),
      "build-host",
    );

    expect(details.item).toEqual([
      { label: "Pull request", value: "#42 Draft", tone: "warning" },
      { label: "Author", value: "alice", tone: "neutral" },
      { label: "Review", value: "Approved", tone: "success" },
      { label: "Changes", value: "", tone: "neutral", diff: { additions: 12, deletions: 3 } },
      { label: "Comments", value: "2", tone: "neutral" },
      { label: "Updated", value: "3h ago", tone: "neutral" },
    ]);
    expect(details.workspace).toEqual([
      {
        label: "Agent",
        value: "Input, 5m ago",
        tone: "waiting",
        notes: [{ text: "claude: review auth", tone: "neutral" }],
      },
      { label: "Worktree", value: "2 ahead, 1 behind PR head, dirty", tone: "neutral" },
      { label: "Host", value: "build-host", tone: "neutral" },
      { label: "Last output", value: "2m ago", tone: "neutral" },
      { label: "Created", value: "2d ago", tone: "neutral" },
      { label: "Path", value: "/tmp/worktrees/ws-1", tone: "neutral" },
    ]);
  });

  it("keeps an issue's state apart from its associated pull request, whose state the list does not carry", () => {
    const details = workspaceHoverDetails(
      workspace({ item_type: "issue", item_number: 7, mr_state: "closed", associated_pr_number: 12 }),
      undefined,
    );

    expect(details.item).toEqual([
      { label: "Issue", value: "#7 Closed", tone: "danger" },
      { label: "Pull request", value: "#12", tone: "neutral" },
    ]);
  });

  it("names a Kata task and an ad-hoc workspace's detected pull request", () => {
    const kata = workspaceHoverDetails(
      workspace({
        item_type: "kata_task",
        item_number: 0,
        kata: { daemon_id: "d", project_uid: "p", issue_uid: "i", short_id: "KT-9" },
      }),
      undefined,
    );
    const adhoc = workspaceHoverDetails(
      workspace({ item_type: "adhoc", item_number: 0, associated_pr_number: 31, mr_state: "merged" }),
      undefined,
    );

    expect(kata.item).toEqual([{ label: "Task", value: "KT-9", tone: "neutral" }]);
    expect(adhoc.item).toEqual([{ label: "Pull request", value: "#31 Merged", tone: "waiting" }]);
  });

  it("leads the workspace rows with a failed setup and its error", () => {
    const details = workspaceHoverDetails(
      workspace({ item_type: "adhoc", item_number: 0, status: "error", error_message: "clone failed" }),
      undefined,
    );

    expect(details.item).toEqual([]);
    expect(labels(details.workspace)).toEqual(["Status", "Created", "Path"]);
    expect(details.workspace[0]).toEqual({
      label: "Status",
      value: "Error",
      tone: "danger",
      notes: [{ text: "clone failed", tone: "neutral" }],
    });
  });
});
