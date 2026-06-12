import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { ActivityItem } from "../api/types.js";
import ActivityFeed from "./ActivityFeed.svelte";

// Component conversion of frontend/tests/e2e-full/activity-row-link.spec.ts:
// flat and threaded activity rows expose an "Open activity" affordance that
// jumps directly to the provider URL without going through the in-app
// selection path (which would open the detail drawer in the real app).

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

const items = vi.hoisted(() => ({ value: [] as ActivityItem[] }));
const viewMode = vi.hoisted(() => ({
  value: "flat" as "flat" | "threaded",
}));
const toggleThreadItem = vi.hoisted(() => vi.fn());

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
      getEnabledEvents: () => new Set(["comment", "review", "commit", "force_push"]),
      getHideClosedMerged: () => false,
      getHideBots: () => false,
      getHideDefaultBranchActivity: () => false,
      getItemFilter: () => "all",
      getActivityItems: () => items.value,
      getActivityError: () => null,
      getViewMode: () => viewMode.value,
      getTimeRange: () => "7d",
      isActivityLoading: () => false,
      isActivityCapped: () => false,
      getCollapseThreads: () => false,
      collapseAllThreads: vi.fn(),
      expandAllThreads: vi.fn(),
      isThreadItemExpanded: () => true,
      toggleThreadItem,
      setActivityFilterTypes: vi.fn(),
      setItemFilter: vi.fn(),
      setEnabledEvents: vi.fn(),
      setHideClosedMerged: vi.fn(),
      setHideBots: vi.fn(),
      setHideDefaultBranchActivity: vi.fn(),
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
      getGroupByRepo: () => false,
      setGroupByRepo: vi.fn(),
      getHideOrgName: () => false,
      setHideOrgName: vi.fn(),
    },
  }),
}));

describe("ActivityFeed row link button", () => {
  beforeEach(() => {
    viewMode.value = "flat";
    items.value = [
      activityItem("widgets-pr1-review", {
        activity_type: "review",
        author: "bob",
        created_at: "2026-04-27T13:00:00Z",
      }),
      activityItem("tools-pr1-comment", {
        created_at: "2026-04-27T12:00:00Z",
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
    ];
  });

  afterEach(() => {
    cleanup();
    toggleThreadItem.mockClear();
    vi.restoreAllMocks();
  });

  it("flat row link button opens the activity URL without triggering row select", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const onSelectItem = vi.fn();

    const { container } = render(ActivityFeed, {
      props: { compact: false, onSelectItem },
    });

    const firstRow = container.querySelector(".activity-table .activity-row");
    expect(firstRow).not.toBeNull();
    const linkBtn = firstRow!.querySelector(".link-btn");
    expect(linkBtn).not.toBeNull();

    await fireEvent.click(linkBtn!);

    expect(open).toHaveBeenCalledTimes(1);
    expect(open).toHaveBeenCalledWith("https://github.com/acme/widgets/pull/1", "_blank", "noopener");
    // Row select (which opens the detail drawer in the app) must not fire.
    expect(onSelectItem).not.toHaveBeenCalled();

    // Contrast: clicking the row body does select the item, proving the link
    // button specifically suppressed a live row-select path.
    await fireEvent.click(firstRow!);
    expect(onSelectItem).toHaveBeenCalledTimes(1);
    expect(onSelectItem).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "widgets-pr1-review",
        item_number: 1,
        item_url: "https://github.com/acme/widgets/pull/1",
      }),
    );
    expect(open).toHaveBeenCalledTimes(1);
  });

  it("threaded item row link button opens the item URL without expanding the item", async () => {
    viewMode.value = "threaded";
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const onSelectItem = vi.fn();

    const { container } = render(ActivityFeed, {
      props: { onSelectItem },
    });

    const itemRow = container.querySelector(".threaded-view .item-row:not(.branch-activity-row)");
    expect(itemRow).not.toBeNull();
    const linkBtn = itemRow!.querySelector(".link-btn");
    expect(linkBtn).not.toBeNull();

    await fireEvent.click(linkBtn!);

    expect(open).toHaveBeenCalledTimes(1);
    // Threaded item-rows link to the underlying item URL (PR or issue).
    expect(open).toHaveBeenCalledWith(
      expect.stringMatching(/github\.com\/acme\/[^/]+\/(pull|issues)\/\d+$/),
      "_blank",
      "noopener",
    );
    expect(open).toHaveBeenCalledWith("https://github.com/acme/widgets/pull/1", "_blank", "noopener");
    // Neither the selection path nor the thread expand toggle fired.
    expect(onSelectItem).not.toHaveBeenCalled();
    expect(toggleThreadItem).not.toHaveBeenCalled();
  });
});
