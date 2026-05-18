import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { PullRequest } from "../../api/types.js";
import PullItem from "./PullItem.svelte";

vi.mock("../../context.js", () => ({
  getStores: () => ({
    pulls: {
      togglePRStar: vi.fn(),
    },
  }),
  getHostState: () => ({}),
}));

function pr(overrides: Partial<PullRequest> = {}): PullRequest {
  return {
    ID: 1,
    Number: 1,
    Title: "Cache widget details",
    Author: "alice",
    State: "open",
    IsDraft: false,
    KanbanStatus: "reviewing",
    LastActivityAt: "2026-05-01T12:00:00Z",
    CIStatus: "",
    MergeableState: "",
    Starred: false,
    labels: [],
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
    worktree_links: [],
    ...overrides,
  } as PullRequest;
}

describe("PullItem", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows the kanban status for open PRs", () => {
    render(PullItem, {
      props: {
        pr: pr(),
        selected: false,
        showRepo: false,
        onclick: vi.fn(),
      },
    });

    expect(screen.getByText("Reviewing")).toBeTruthy();
  });

  it("hides the kanban status for closed and merged PRs", () => {
    const { rerender } = render(PullItem, {
      props: {
        pr: pr({ State: "closed" }),
        selected: false,
        showRepo: false,
        onclick: vi.fn(),
      },
    });

    expect(screen.queryByText("Reviewing")).toBeNull();

    rerender({
      pr: pr({ State: "merged", KanbanStatus: "awaiting_merge" }),
      selected: false,
      showRepo: false,
      onclick: vi.fn(),
    });

    expect(screen.queryByText("Ready")).toBeNull();
  });
});
