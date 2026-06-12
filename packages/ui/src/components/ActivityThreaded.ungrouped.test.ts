import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { ActivityItem } from "../api/types.js";
import ActivityThreaded from "./ActivityThreaded.svelte";

// Component conversion of the ungrouped threaded cases from
// frontend/tests/e2e-full/grouping-toggle.spec.ts: cross-repo items with the
// same number stay separate threads, and an empty result set renders the
// threaded view's own empty state.

function activityItem(id: string, overrides: Partial<ActivityItem> = {}): ActivityItem {
  return {
    id,
    cursor: id,
    activity_type: "comment",
    author: "alice",
    body_preview: "",
    created_at: "2026-04-27T12:00:00Z",
    item_author: "alice",
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

vi.mock("../context.js", () => ({
  getStores: () => ({
    grouping: {
      getGroupByRepo: () => false,
      getHideOrgName: () => false,
    },
    activity: {
      isThreadItemExpanded: () => true,
      toggleThreadItem: vi.fn(),
    },
  }),
}));

describe("ActivityThreaded ungrouped", () => {
  afterEach(() => {
    cleanup();
  });

  it("keeps cross-repo items with the same number separate", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          activityItem("widgets-pr1"),
          activityItem("tools-pr1", {
            created_at: "2026-04-27T11:00:00Z",
            item_title: "Fix flaky tool runner",
            item_url: "https://github.com/acme/tools/pull/1",
            repo_name: "tools",
            repo: {
              provider: "github",
              platform_host: "github.com",
              owner: "acme",
              name: "tools",
              repo_path: "acme/tools",
            },
          }),
        ],
        onSelectItem: undefined,
      },
    });

    const rows = Array.from(container.querySelectorAll(".item-row:not(.branch-activity-row)"));
    expect(rows).toHaveLength(2);

    // Both threads keep the same #1 ref (not merged into one thread).
    const refs = rows.map((row) => row.querySelector(".item-ref")?.textContent);
    expect(refs).toEqual(["#1", "#1"]);

    // And each belongs to its own repo.
    const repoLabels = rows.map((row) => row.querySelector(".repo-chip__label")?.textContent);
    expect(repoLabels).toEqual(["acme/widgets", "acme/tools"]);
  });

  it("renders its own empty state when there are no items", () => {
    const { container } = render(ActivityThreaded, {
      props: { items: [], onSelectItem: undefined },
    });

    const empty = container.querySelector(".threaded-view .empty-state");
    expect(empty?.textContent).toBe("No activity found");
    expect(container.querySelectorAll(".item-row")).toHaveLength(0);
  });
});
