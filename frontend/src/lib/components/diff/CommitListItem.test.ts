import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import type { CommitInfo } from "../../api/types.js";
import CommitListItem from "./CommitListItem.svelte";

function makeCommit(overrides: Partial<CommitInfo> = {}): CommitInfo {
  return {
    sha: "a5a7f26abcdef0123456789",
    message: "Document analyze precedence",
    author_name: "Alice",
    authored_at: "2026-06-27T00:00:00Z",
    ...overrides,
  };
}

function renderItem(commit: CommitInfo, showStats = false): void {
  render(CommitListItem, {
    props: { commit, showStats, active: false, onclick: () => {} },
  });
}

describe("CommitListItem", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows shared line counts, including compact large values and exact accessible totals", () => {
    renderItem(makeCommit({ stats: { additions: 12345, deletions: 42 } }), true);
    expect(screen.getByLabelText("12345 additions, 42 deletions")).toBeTruthy();
    expect(screen.getByText("+12.3k")).toBeTruthy();
    expect(screen.getByText("−42")).toBeTruthy();
  });

  it("distinguishes an empty diff from unavailable counts", () => {
    renderItem(makeCommit({ stats: { additions: 0, deletions: 0 } }), true);
    expect(screen.getByLabelText("0 additions, 0 deletions")).toBeTruthy();
    cleanup();
    renderItem(makeCommit(), true);
    expect(document.querySelector(".kit-diff-stats")).toBeNull();
  });

  it("keeps counts out of other commit lists unless requested", () => {
    renderItem(makeCommit({ stats: { additions: 12, deletions: 3 } }));
    expect(document.querySelector(".kit-diff-stats")).toBeNull();
  });

  it("flags commits that have not been pushed", () => {
    renderItem(makeCommit({ pushed: false }));
    expect(screen.getByLabelText("Not pushed to remote").classList.contains("kit-status-dot--stale")).toBe(true);
  });

  it("does not flag pushed commits", () => {
    renderItem(makeCommit({ pushed: true }));
    expect(screen.queryByLabelText("Not pushed to remote")).toBeNull();
  });

  it("does not flag commits with unknown push status", () => {
    renderItem(makeCommit());
    expect(screen.queryByLabelText("Not pushed to remote")).toBeNull();
  });
});
