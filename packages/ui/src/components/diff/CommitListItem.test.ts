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

function renderItem(commit: CommitInfo): void {
  render(CommitListItem, {
    props: { commit, active: false, onclick: () => {} },
  });
}

describe("CommitListItem", () => {
  afterEach(() => {
    cleanup();
  });

  it("flags commits that have not been pushed", () => {
    renderItem(makeCommit({ pushed: false }));
    expect(screen.getByRole("img", { name: "Not pushed to remote" })).toBeTruthy();
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
