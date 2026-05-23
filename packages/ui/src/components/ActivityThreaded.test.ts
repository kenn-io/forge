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
const toggleThreadItem = vi.hoisted(() => vi.fn());

vi.mock("../context.js", () => ({
  getStores: () => ({
    grouping: { getGroupByRepo: () => false },
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

  it("groups branch activity by repo branch without an item number", async () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          branchActivityItem("c1"),
          branchActivityItem("c2", {
            created_at: "2026-04-27T11:59:00Z",
            commit_sha: "b1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
          }),
        ],
        onSelectItem: undefined,
      },
    });

    expect(container.querySelectorAll(".item-row")).toHaveLength(1);
    expect(container.textContent).toContain("main updates on acme/widgets");
    expect(container.textContent).not.toContain("#0");

    const caret = container.querySelector(".thread-caret");
    expect(caret).not.toBeNull();
    await fireEvent.click(caret!);
    expect(toggleThreadItem).toHaveBeenCalledWith(
      "github|github.com|acme/widgets:branch:main",
    );
  });
});
