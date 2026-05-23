import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ActivityItem } from "../api/types.js";
import ActivityFeed from "./ActivityFeed.svelte";

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
    ...overrides,
  };
}

const items = vi.hoisted(() => ({ value: [] as ActivityItem[] }));
const viewMode = vi.hoisted(() => ({ value: "flat" as "flat" | "threaded" }));
const collapseThreads = vi.hoisted(() => ({ value: false }));
const collapseAllThreads = vi.hoisted(() => vi.fn());
const expandAllThreads = vi.hoisted(() => vi.fn());

vi.mock("../context.js", () => ({
  getNavigate: () => vi.fn(),
  getSidebar: () => ({ isEmbedded: () => false }),
  getStores: () => ({
    activity: {
      initializeFromMount: vi.fn(),
      loadActivity: vi.fn(async () => undefined),
      startActivityPolling: vi.fn(),
      stopActivityPolling: vi.fn(),
      getActivitySearch: () => "",
      getEnabledEvents: () =>
        new Set(["comment", "review", "commit", "force_push"]),
      getHideClosedMerged: () => false,
      getHideBots: () => false,
      getItemFilter: () => "all",
      getActivityItems: () => items.value,
      getActivityError: () => null,
      getViewMode: () => viewMode.value,
      getTimeRange: () => "7d",
      isActivityLoading: () => false,
      isActivityCapped: () => false,
      getCollapseThreads: () => collapseThreads.value,
      collapseAllThreads,
      expandAllThreads,
      isThreadItemExpanded: () => true,
      toggleThreadItem: vi.fn(),
      setActivityFilterTypes: vi.fn(),
      setItemFilter: vi.fn(),
      setEnabledEvents: vi.fn(),
      setHideClosedMerged: vi.fn(),
      setHideBots: vi.fn(),
      setActivitySearch: vi.fn(),
      setTimeRange: vi.fn(),
      setViewMode: vi.fn(),
      syncToURL: vi.fn(),
    },
    settings: {
      isSettingsLoaded: () => true,
      hasConfiguredRepos: () => true,
    },
    sync: {
      subscribeSyncComplete: vi.fn(() => () => undefined),
    },
    grouping: {
      getGroupByRepo: () => true,
      setGroupByRepo: vi.fn(),
    },
  }),
}));

describe("ActivityFeed compact mode", () => {
  beforeEach(() => {
    viewMode.value = "flat";
    collapseThreads.value = false;
    items.value = [
      activityItem("selected"),
      activityItem("other", {
        item_number: 2,
        item_title: "Fix Safari issue",
        item_type: "issue",
        item_url: "https://github.com/acme/widgets/issues/2",
      }),
    ];
  });

  afterEach(() => {
    cleanup();
  });

  it("renders compact rows instead of the wide table", () => {
    const { container } = render(ActivityFeed, {
      props: {
        compact: true,
        selectedItem: {
          itemType: "pr",
          owner: "acme",
          name: "widgets",
          number: 1,
        },
      },
    });

    expect(container.querySelector(".activity-table")).toBeNull();
    expect(container.querySelectorAll(".activity-compact-row")).toHaveLength(2);
    expect(screen.getByText("Add widget caching layer")).toBeTruthy();
  });

  it("highlights all compact rows for the selected item", () => {
    items.value = [
      activityItem("comment", { activity_type: "comment" }),
      activityItem("review", { id: "review", activity_type: "review" }),
      activityItem("other", {
        id: "other",
        item_number: 2,
        item_title: "Other PR",
      }),
    ];

    const { container } = render(ActivityFeed, {
      props: {
        compact: true,
        selectedItem: {
          itemType: "pr",
          owner: "acme",
          name: "widgets",
          number: 1,
        },
      },
    });

    expect(
      container.querySelectorAll(".activity-compact-row.selected"),
    ).toHaveLength(2);
  });

  it("hides the collapse-all control in flat mode", () => {
    render(ActivityFeed, { props: { compact: true } });
    expect(
      screen.queryByRole("button", { name: /Collapse all|Expand all/ }),
    ).toBeNull();
  });

  it("uses shared semantic chips for compact item kind and state", () => {
    items.value = [
      activityItem("merged", {
        item_state: "merged",
      }),
    ];

    const { container } = render(ActivityFeed, {
      props: {
        compact: true,
      },
    });

    const row = container.querySelector(".activity-compact-row");
    expect(row?.querySelector(".chip--kind-pr")?.textContent?.trim())
      .toBe("PR");
    expect(row?.querySelector(".chip--state-merged")?.textContent)
      .toContain("Merged");
    expect(row?.querySelector(".badge")).not.toBeNull();
    expect(row?.querySelector(".state-badge")).not.toBeNull();
  });
});

describe("ActivityFeed collapse-all control", () => {
  beforeEach(() => {
    viewMode.value = "threaded";
    collapseThreads.value = false;
    items.value = [];
  });

  afterEach(() => {
    cleanup();
    collapseAllThreads.mockClear();
    expandAllThreads.mockClear();
  });

  it("shows Collapse all and triggers collapseAllThreads when expanded", async () => {
    render(ActivityFeed, { props: {} });
    const btn = screen.getByRole("button", { name: "Collapse all" });
    await fireEvent.click(btn);
    expect(collapseAllThreads).toHaveBeenCalledTimes(1);
    expect(expandAllThreads).not.toHaveBeenCalled();
  });

  it("shows Expand all and triggers expandAllThreads when collapsed", async () => {
    collapseThreads.value = true;
    render(ActivityFeed, { props: {} });
    const btn = screen.getByRole("button", { name: "Expand all" });
    await fireEvent.click(btn);
    expect(expandAllThreads).toHaveBeenCalledTimes(1);
    expect(collapseAllThreads).not.toHaveBeenCalled();
  });
});
