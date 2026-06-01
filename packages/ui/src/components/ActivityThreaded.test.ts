import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ActivityItem } from "../api/types.js";
import ActivityThreaded from "./ActivityThreaded.svelte";

function activityItem(
  id: string,
  overrides: Partial<ActivityItem> = {},
): ActivityItem {
  return {
    id,
    cursor: id,
    activity_type: "comment",
    author: "alice",
    body_preview: "",
    created_at: "2026-04-27T12:00:00Z",
    item_number: 1,
    item_state: "open",
    item_title: "Add widget caching layer",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/1",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
    ...overrides,
  };
}

function branchActivityItem(
  id: string,
  overrides: Partial<ActivityItem> = {},
): ActivityItem {
  return activityItem(id, {
    activity_type: "default_branch_commit",
    author: "alice",
    author_name: "Alice Example",
    body_preview: "Refresh cache warmer",
    branch_name: "main",
    commit_sha: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
    committed_at: "2026-04-27T12:00:00Z",
    item_number: 0,
    item_state: "",
    item_title: "",
    item_type: "",
    item_url: "",
    activity_url: "https://github.com/acme/widgets/commit/a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
    ...overrides,
  });
}

const expanded = vi.hoisted(() => ({ value: true }));
const groupByRepo = vi.hoisted(() => ({ value: false }));
const hideOrgName = vi.hoisted(() => ({ value: false }));
const toggleThreadItem = vi.hoisted(() => vi.fn());

vi.mock("../context.js", () => ({
  getStores: () => ({
    grouping: {
      getGroupByRepo: () => groupByRepo.value,
      getHideOrgName: () => hideOrgName.value,
    },
    activity: {
      isThreadItemExpanded: () => expanded.value,
      toggleThreadItem,
    },
  }),
}));

describe("ActivityThreaded collapse", () => {
  afterEach(() => {
    cleanup();
    expanded.value = true;
    groupByRepo.value = false;
    hideOrgName.value = false;
    toggleThreadItem.mockClear();
  });

  it("shows events when the item is expanded", () => {
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    expect(container.querySelectorAll(".event-row").length).toBeGreaterThan(0);
  });

  it("hides events but keeps the item row when collapsed", () => {
    expanded.value = false;
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    expect(container.querySelectorAll(".event-row")).toHaveLength(0);
    expect(container.querySelectorAll(".item-row")).toHaveLength(1);
  });

  it("toggles the item on caret click without selecting the row", async () => {
    const onSelectItem = vi.fn();
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem },
    });
    const caret = container.querySelector(".thread-caret");
    expect(caret).not.toBeNull();
    await fireEvent.click(caret!);
    expect(toggleThreadItem).toHaveBeenCalledTimes(1);
    expect(toggleThreadItem).toHaveBeenCalledWith(
      "github|github.com|acme/widgets:pr:1",
    );
    expect(onSelectItem).not.toHaveBeenCalled();
  });

  it("renders the repo chip label in non-grouped mode", () => {
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    const label = container.querySelector(
      ".repo-chip.repo-tag .repo-chip__label",
    );
    expect(label?.textContent).toBe("acme/widgets");
  });

  it("renders branch activity as top-level rows interleaved with item threads", async () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          branchActivityItem("c4", {
            created_at: "2026-04-27T12:04:00Z",
            commit_sha: "d1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
          }),
          branchActivityItem("c3", {
            created_at: "2026-04-27T12:03:00Z",
            commit_sha: "c1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
          }),
          branchActivityItem("c2", {
            created_at: "2026-04-27T12:02:00Z",
            commit_sha: "b1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
          }),
          activityItem("pr-comment", {
            created_at: "2026-04-27T12:01:30Z",
          }),
          branchActivityItem("c1", {
            created_at: "2026-04-27T12:01:00Z",
          }),
        ],
        onSelectItem: undefined,
      },
    });

    const rows = Array.from(container.querySelectorAll(".item-row"));
    expect(rows).toHaveLength(3);
    expect(rows[0]?.textContent).toContain("3 commits");
    expect(rows[1]?.textContent).toContain("Add widget caching layer");
    expect(rows[2]?.textContent).toContain("Refresh cache warmer");
    expect(container.textContent).not.toContain("main updates on acme/widgets");
    expect(container.textContent).not.toContain("#0");
    expect(
      container.querySelector(".branch-activity-row .thread-caret"),
    ).toBeNull();
  });

  it("labels commit rows without the branch type or duplicated commit text", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [branchActivityItem("c1")],
        onSelectItem: undefined,
      },
    });

    const row = container.querySelector(".branch-activity-row");
    expect(row).not.toBeNull();
    expect(row?.textContent).toContain("Commit");
    expect(row?.textContent).toContain("acme/widgets");
    expect(row?.textContent).toContain("main");
    expect(row?.textContent).toContain("Refresh cache warmer");
    expect(row?.textContent).not.toContain("Branch");
    expect(row?.textContent).not.toContain("Commit Commit");
    expect(row?.textContent).not.toContain("a1b2c3d");
  });

  it("selects default branch commit rows for an in-app diff", async () => {
    const onSelectItem = vi.fn();
    const onSelectBranchCommit = vi.fn();
    const open = vi
      .spyOn(window, "open")
      .mockImplementation(() => null);

    const { container } = render(ActivityThreaded, {
      props: {
        items: [branchActivityItem("c1")],
        onSelectItem,
        onSelectBranchCommit,
      },
    });

    const row = container.querySelector(".branch-activity-row");
    expect(row).not.toBeNull();
    await fireEvent.click(row!);

    expect(onSelectItem).not.toHaveBeenCalled();
    expect(onSelectBranchCommit).toHaveBeenCalledWith(
      expect.objectContaining({
        activity_type: "default_branch_commit",
        commit_sha: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
      }),
    );
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it("highlights the selected default branch commit row", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [branchActivityItem("c1")],
        onSelectItem: undefined,
        selectedBranchCommit: {
          provider: "github",
          platformHost: "github.com",
          repoPath: "acme/widgets",
          owner: "acme",
          name: "widgets",
          commitSha: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
        },
      },
    });

    expect(
      container.querySelector(".branch-activity-row.selected"),
    ).not.toBeNull();
  });

  it("shows the PR author on the item row, not the latest actor", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          activityItem("c-late", {
            created_at: "2026-04-27T13:00:00Z",
            author: "bob",
            author_name: "Bob Example",
            item_author: "prauthor",
          }),
          activityItem("c-early", {
            created_at: "2026-04-27T12:00:00Z",
            author: "alice",
            author_name: "Alice Example",
            item_author: "prauthor",
          }),
        ],
        onSelectItem: undefined,
      },
    });

    const row = container.querySelector(".item-row:not(.branch-activity-row)");
    expect(row).not.toBeNull();
    const authorCell = row?.querySelector(".cell--author");
    expect(authorCell?.textContent?.trim()).toBe("prauthor");

    // Expanded event rows still attribute each event to its own actor.
    const eventAuthors = Array.from(
      container.querySelectorAll(".event-row .event-author"),
    ).map((el) => el.textContent?.trim());
    expect(eventAuthors).toEqual(["Bob Example", "Alice Example"]);
  });

  it("shows a workspace indicator on item threads with attached workspaces", () => {
    const { getByLabelText } = render(ActivityThreaded, {
      props: {
        items: [
          activityItem("c1", {
            workspace: { id: "ws-pr-1", status: "ready" },
          }),
        ],
        onSelectItem: undefined,
      },
    });

    expect(getByLabelText("Workspace attached (ready)")).toBeTruthy();
  });

  it("shows the commit author on branch rows", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [branchActivityItem("c1")],
        onSelectItem: undefined,
      },
    });

    const row = container.querySelector(".branch-activity-row");
    const authorCell = row?.querySelector(".cell--author");
    expect(authorCell?.textContent?.trim()).toBe("Alice Example");
  });

  it("shows just the repo name when hideOrgName is on", () => {
    hideOrgName.value = true;
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    const label = container.querySelector(
      ".repo-chip.repo-tag .repo-chip__label",
    );
    expect(label?.textContent).toBe("widgets");
  });

  it("shows just the repo name in grouped headers when hideOrgName is on", () => {
    groupByRepo.value = true;
    hideOrgName.value = true;

    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });

    const repoName = container.querySelector(".repo-header .repo-name");
    expect(repoName?.textContent).toBe("widgets");
    expect(container.textContent).not.toContain("acme/widgets");
  });

  it("keeps force-push rows as provider compare links", async () => {
    const onSelectBranchCommit = vi.fn();
    const open = vi
      .spyOn(window, "open")
      .mockImplementation(() => null);

    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          branchActivityItem("force-1", {
            activity_type: "default_branch_force_push",
            activity_url:
              "https://github.com/acme/widgets/compare/aaaaaaaa...bbbbbbbb",
            before_sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            after_sha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            body_preview:
              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -> bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            commit_sha: "",
          }),
        ],
        onSelectItem: undefined,
        onSelectBranchCommit,
      },
    });

    const row = container.querySelector(".branch-activity-row");
    expect(row).not.toBeNull();
    await fireEvent.click(row!);

    expect(onSelectBranchCommit).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledWith(
      "https://github.com/acme/widgets/compare/aaaaaaaa...bbbbbbbb",
      "_blank",
      "noopener",
    );
    open.mockRestore();
  });
});
