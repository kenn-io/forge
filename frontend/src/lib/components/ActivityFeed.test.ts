import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { ActivityItem, ActivitySubject, WorkspaceActivitySubject } from "../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import { cycleLabelFilter as cycleLabel, type LabelFilters } from "../utils/label-filter.js";

const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));

vi.mock("../app/runtime-context.js", () => ({
  getAppRuntime: () => {
    const runtime = runtimeCapture.current;
    if (runtime === undefined) throw new Error("activity feed test runtime is not initialized");
    return runtime;
  },
}));

import ActivityFeed from "./ActivityFeed.svelte";

beforeEach(() => {
  runtimeCapture.current = makeAppRuntime();
  itemActivity.value = [];
  workspaceActivity.value = [];
  hideBots.value = false;
  hideDrafts.value = false;
  mergeQueued.value = undefined;
  labelFilters.value = new Map();
  cycleLabelFilter.mockClear();
  setHideDrafts.mockClear();
  useWorkspaceActivityForRecency.value = false;
});

afterEach(async () => {
  if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
  runtimeCapture.current = undefined;
});

function activityItem(id: string, overrides: Partial<ActivityItem> = {}): ActivityItem {
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

function branchActivityItem(id: string, overrides: Partial<ActivityItem> = {}): ActivityItem {
  return activityItem(id, {
    activity_type: "default_branch_commit",
    author: "Alice Example",
    commit_author: "alice",
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

function workspaceSubject(overrides: Partial<WorkspaceActivitySubject> = {}): WorkspaceActivitySubject {
  return {
    activity_at: "2026-08-09T12:00:00Z",
    item_author: "alice",
    item_number: 7,
    item_state: "open",
    item_title: "Workspace-only work",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/7",
    platform_host: "github.com",
    repo: activityItem("repo-source").repo!,
    repo_name: "widgets",
    repo_owner: "acme",
    workspace: { id: "ws-7", status: "ready" },
    ...overrides,
  };
}

const items = vi.hoisted(() => ({ value: [] as ActivityItem[] }));
const itemActivity = vi.hoisted(() => ({ value: [] as ActivitySubject[] }));
const workspaceActivity = vi.hoisted(() => ({ value: [] as WorkspaceActivitySubject[] }));
const viewMode = vi.hoisted(() => ({
  value: "flat" as "flat" | "threaded",
}));
const collapseThreads = vi.hoisted(() => ({ value: false }));
const collapseAllThreads = vi.hoisted(() => vi.fn());
const expandAllThreads = vi.hoisted(() => vi.fn());
const rollUpCommits = vi.hoisted(() => ({ value: false }));
const hideDefaultBranchActivity = vi.hoisted(() => ({ value: false }));
const hideClosedMerged = vi.hoisted(() => ({ value: false }));
const hideBots = vi.hoisted(() => ({ value: false }));
const hideDrafts = vi.hoisted(() => ({ value: false }));
const mergeQueued = vi.hoisted(() => ({ value: undefined as "require" | "exclude" | undefined }));
const onlyClosedMerged = vi.hoisted(() => ({ value: false }));
const onlyBots = vi.hoisted(() => ({ value: false }));
const onlyDrafts = vi.hoisted(() => ({ value: false }));
const pullAttributeFilters = vi.hoisted(() => ({ value: [] as string[] }));
const labelFilters = vi.hoisted(() => ({ value: new Map() as LabelFilters }));
const cycleLabelFilter = vi.hoisted(() =>
  vi.fn((name: string) => {
    labelFilters.value = cycleLabel(labelFilters.value, name);
  }),
);
const setHideDrafts = vi.hoisted(() =>
  vi.fn((value: boolean) => {
    hideDrafts.value = value;
  }),
);
const useWorkspaceActivityForRecency = vi.hoisted(() => ({ value: false }));
const hideOrgName = vi.hoisted(() => ({ value: false }));
const setActivityFilterTypes = vi.hoisted(() => vi.fn());
const enabledItemTypes = vi.hoisted(() => ({
  value: new Set<"pr" | "issue">(["pr", "issue"]),
}));
const enabledEvents = vi.hoisted(() => ({
  value: new Set(["comment", "review", "commit", "force_push"]),
}));
const showNotifications = vi.hoisted(() => ({ value: true }));
const activityCapped = vi.hoisted(() => ({ value: false }));
const itemActivityCapped = vi.hoisted(() => ({ value: false }));
const olderItemsAvailable = vi.hoisted(() => ({ value: false }));
const canLoadMore = vi.hoisted(() => ({ value: false }));
const loadingMore = vi.hoisted(() => ({ value: false }));
const loadMoreActivity = vi.hoisted(() => vi.fn());
const loadOlderItems = vi.hoisted(() => vi.fn());
const involvesMe = vi.hoisted(() => ({ value: undefined as "require" | "exclude" | undefined }));
const unassigned = vi.hoisted(() => ({ value: undefined as "require" | "exclude" | undefined }));
const markNotificationSeen = vi.hoisted(() => vi.fn(async () => undefined));
const loadActivity = vi.hoisted(() => vi.fn(async () => undefined));
const threadLoadError = vi.hoisted(() => ({ value: null as string | null }));
const retryFailedThreadLoads = vi.hoisted(() => vi.fn());
const selectedAuthor = vi.hoisted(() => ({ value: undefined as string | undefined }));
const setActivityAuthor = vi.hoisted(() =>
  vi.fn((author: string | undefined) => {
    selectedAuthor.value = author;
  }),
);

vi.mock("../context.js", () => ({
  getNavigate: () => vi.fn(),
  getStores: () => ({
    activity: {
      initializeFromMount: vi.fn(),
      loadActivity,
      ensureActivityLoaded: loadActivity,
      setActivityPageLimit: vi.fn(),
      startActivityPolling: vi.fn(),
      stopActivityPolling: vi.fn(),
      getActivitySearch: () => "",
      getActivityAuthor: () => selectedAuthor.value,
      getActivityAuthors: () => ["Alice", "Bob"],
      isActivityAuthorsLoading: () => false,
      getActivityAuthorsError: () => null,
      getEnabledEvents: () => enabledEvents.value,
      getShowNotifications: () => showNotifications.value,
      getStarred: () => false,
      setStarred: vi.fn(),
      getReferencedByPR: () => undefined,
      setReferencedByPR: vi.fn(),
      getHideBotAuthoredIssues: () => false,
      setHideBotAuthoredIssues: vi.fn(),
      getTimeRangeDefault: () => "7d",
      getViewModeDefault: () => "flat",
      getInvolvesMe: () => involvesMe.value,
      getUnassigned: () => unassigned.value,
      getHideClosedMerged: () => hideClosedMerged.value,
      getHideBots: () => hideBots.value,
      getHideDrafts: () => hideDrafts.value,
      getMergeQueued: () => mergeQueued.value,
      getActivityState: () => (onlyClosedMerged.value ? "closed" : hideClosedMerged.value ? "open" : "all"),
      getBots: () => (onlyBots.value ? "require" : hideBots.value ? "exclude" : undefined),
      getDraft: () => (onlyDrafts.value ? "require" : hideDrafts.value ? "exclude" : undefined),
      getPullAttributeFilters: () => pullAttributeFilters.value,
      getLabelFilters: () => labelFilters.value,
      cycleLabelFilter,
      clearLabelFilters: vi.fn(() => {
        labelFilters.value = new Map();
      }),
      getUseWorkspaceActivityForRecency: () => useWorkspaceActivityForRecency.value,
      getHideDefaultBranchActivity: () => hideDefaultBranchActivity.value,
      getEnabledItemTypes: () => enabledItemTypes.value,
      getActivityItems: () => items.value,
      getItemActivity: () => itemActivity.value,
      getWorkspaceActivity: () => workspaceActivity.value,
      getActivityError: () => null,
      getThreadLoadError: () => threadLoadError.value,
      getViewMode: () => viewMode.value,
      getTimeRange: () => "7d",
      isActivityLoading: () => false,
      isActivityCapped: () => activityCapped.value,
      isItemActivityCapped: () => itemActivityCapped.value,
      canLoadOlderItems: () => olderItemsAvailable.value,
      isLoadingOlderItems: () => false,
      loadOlderItems,
      canLoadMoreActivity: () => canLoadMore.value,
      isLoadingMoreActivity: () => loadingMore.value,
      loadMoreActivity,
      getCollapseThreads: () => collapseThreads.value,
      collapseAllThreads,
      expandAllThreads,
      getRollUpCommits: () => rollUpCommits.value,
      setRollUpCommits: vi.fn((value: boolean) => {
        rollUpCommits.value = value;
      }),
      isThreadItemExpanded: () => true,
      toggleThreadItem: vi.fn(),
      retryFailedThreadLoads,
      setActivityFilterTypes,
      setEnabledItemTypes: vi.fn((itemTypes: Set<"pr" | "issue">) => {
        enabledItemTypes.value = itemTypes;
      }),
      setEnabledEvents: vi.fn((events: Set<string>) => {
        enabledEvents.value = events;
      }),
      setShowNotifications: vi.fn((value: boolean) => {
        showNotifications.value = value;
      }),
      setInvolvesMe: vi.fn((value: "require" | "exclude" | undefined) => {
        involvesMe.value = value;
      }),
      setUnassigned: vi.fn((value: "require" | "exclude" | undefined) => {
        unassigned.value = value;
      }),
      markNotificationSeen,
      setHideClosedMerged: vi.fn(),
      setHideBots: vi.fn(),
      setHideDrafts,
      setActivityState: vi.fn((state: "open" | "closed" | "all") => {
        onlyClosedMerged.value = state === "closed";
        hideClosedMerged.value = state === "open";
      }),
      setBots: vi.fn((value: "require" | "exclude" | undefined) => {
        onlyBots.value = value === "require";
        hideBots.value = value === "exclude";
      }),
      setDraft: vi.fn((value: "require" | "exclude" | undefined) => {
        onlyDrafts.value = value === "require";
        setHideDrafts(value === "exclude");
      }),
      setPullAttributeFilters: vi.fn((next: string[]) => {
        pullAttributeFilters.value = next;
      }),
      setMergeQueued: (value: "require" | "exclude" | undefined) => {
        mergeQueued.value = value;
      },
      setHideDefaultBranchActivity: vi.fn((value: boolean) => {
        hideDefaultBranchActivity.value = value;
      }),
      setActivitySearch: vi.fn(),
      setActivityAuthor,
      setTimeRange: vi.fn(),
      setViewMode: vi.fn(),
      syncToURL: vi.fn(),
    },
    issues: { canFilterReferencedByPR: () => true },
    settings: {
      getWorkspaceSettings: () => ({ show_agent_status_in_lists: false }),
      isSettingsLoaded: () => true,
      hasConfiguredRepos: () => true,
    },
    sync: {
      subscribeSyncCompleteWithoutLiveUpdates: vi.fn(() => () => undefined),
    },
    grouping: {
      getGroupByRepo: () => true,
      setGroupByRepo: vi.fn(),
      getHideOrgName: () => hideOrgName.value,
      setHideOrgName: vi.fn((value: boolean) => {
        hideOrgName.value = value;
      }),
    },
  }),
}));

describe("ActivityFeed compact mode", () => {
  beforeEach(() => {
    viewMode.value = "flat";
    collapseThreads.value = false;
    rollUpCommits.value = false;
    hideDefaultBranchActivity.value = false;
    hideClosedMerged.value = false;
    onlyClosedMerged.value = false;
    onlyBots.value = false;
    onlyDrafts.value = false;
    pullAttributeFilters.value = [];
    hideOrgName.value = false;
    enabledItemTypes.value = new Set(["pr", "issue"]);
    enabledEvents.value = new Set(["comment", "review", "commit", "force_push"]);
    showNotifications.value = true;
    involvesMe.value = undefined;
    unassigned.value = undefined;
    activityCapped.value = false;
    itemActivityCapped.value = false;
    olderItemsAvailable.value = false;
    canLoadMore.value = false;
    loadingMore.value = false;
    loadMoreActivity.mockClear();
    loadOlderItems.mockClear();
    selectedAuthor.value = undefined;
    setActivityAuthor.mockClear();
    setActivityFilterTypes.mockClear();
    loadActivity.mockClear();
    threadLoadError.value = null;
    retryFailedThreadLoads.mockClear();
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

  it.each([false, true])("keeps paging controls without a ceiling notice (pending: %s)", async (pending) => {
    activityCapped.value = true;
    canLoadMore.value = true;
    loadingMore.value = pending;
    const { container } = render(ActivityFeed);
    if (pending) {
      expect(container.querySelector(".list-pagination button")?.hasAttribute("disabled")).toBe(true);
    } else {
      await fireEvent.click(screen.getByRole("button", { name: "Load more" }));
      expect(loadMoreActivity).toHaveBeenCalledOnce();
    }
    expect(screen.queryByText(/most recent 5,000 events/)).toBeNull();
  });

  it("keeps older activity available when the loaded page has no matches", () => {
    activityCapped.value = true;
    // Bots: required hides the loaded human-authored rows in the browser.
    onlyBots.value = true;
    canLoadMore.value = true;
    render(ActivityFeed);
    expect(screen.queryByText("No activity found")).toBeNull();
    expect(screen.getByText("No matches in loaded activity")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Load more" })).toBeTruthy();
  });

  it("shows the event ceiling notice after paging ends", () => {
    activityCapped.value = true;
    viewMode.value = "threaded";
    render(ActivityFeed);
    expect(screen.getByText(/most recent 5,000 events/)).toBeTruthy();
  });

  it("shows the older item control in threaded view", async () => {
    itemActivityCapped.value = true;
    olderItemsAvailable.value = true;
    viewMode.value = "threaded";
    render(ActivityFeed);
    expect(screen.getByText(/Item totals cover loaded items only/)).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Load older items" }));
    expect(loadOlderItems).toHaveBeenCalledOnce();
  });

  it("hides the older item control in flat view", () => {
    itemActivityCapped.value = true;
    olderItemsAvailable.value = true;
    viewMode.value = "flat";
    render(ActivityFeed);
    expect(screen.queryByText(/Item totals cover loaded items only/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Load older items" })).toBeNull();
  });

  it("offers to retry a failed thread history load", async () => {
    threadLoadError.value = "thread history unavailable";

    render(ActivityFeed, { props: { compact: true } });

    expect(screen.getByText("thread history unavailable")).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Retry thread history" }));
    expect(retryFailedThreadLoads).toHaveBeenCalledOnce();
  });

  it("shows workspace-only subjects only in threaded mode", () => {
    useWorkspaceActivityForRecency.value = true;
    items.value = [];
    workspaceActivity.value = [workspaceSubject()];

    const flat = render(ActivityFeed, { props: { compact: true } });
    expect(flat.container.textContent).not.toContain("Workspace-only work");
    cleanup();

    viewMode.value = "threaded";
    const threaded = render(ActivityFeed, { props: { compact: true } });
    expect(threaded.container.textContent).toContain("Workspace-only work");
  });

  it("hides cached workspace-only subjects when workspace recency is disabled", () => {
    viewMode.value = "threaded";
    workspaceActivity.value = [workspaceSubject()];

    const { container } = render(ActivityFeed, { props: { compact: true } });

    expect(container.textContent).not.toContain("Workspace-only work");
  });

  it("applies item-scope and closed filters to workspace summaries", () => {
    useWorkspaceActivityForRecency.value = true;
    viewMode.value = "threaded";
    items.value = [];
    workspaceActivity.value = [workspaceSubject({ item_state: "closed" })];
    hideClosedMerged.value = true;

    const { container } = render(ActivityFeed, { props: { compact: true } });
    expect(container.textContent).not.toContain("Workspace-only work");
  });

  it("hides bot-authored parent and workspace summaries in threaded mode", () => {
    useWorkspaceActivityForRecency.value = true;
    viewMode.value = "threaded";
    items.value = [];
    itemActivity.value = [
      workspaceSubject({
        item_author: "renovate[bot]",
        item_number: 8,
        item_title: "Bot-authored parent",
        workspace: undefined,
      }),
    ];
    workspaceActivity.value = [
      workspaceSubject({
        item_author: "release-bot",
        item_number: 9,
        item_title: "Bot-authored workspace",
      }),
    ];
    hideBots.value = true;

    const { container } = render(ActivityFeed, { props: { compact: true } });

    expect(container.textContent).not.toContain("Bot-authored parent");
    expect(container.textContent).not.toContain("Bot-authored workspace");
  });

  it("selects a single PR or issue author from the Author flyout", async () => {
    render(ActivityFeed, { props: { compact: false } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: /^Author/ }));
    const flyout = screen.getByRole("dialog", { name: "Author" });
    expect(within(flyout).getByRole("button", { name: "Anyone" }).getAttribute("aria-pressed")).toBe("true");
    await fireEvent.click(within(flyout).getByRole("button", { name: "Alice" }));

    expect(setActivityAuthor).toHaveBeenCalledWith("Alice");
  });

  it("counts each item once in the Labels flyout and cycles the clicked label", async () => {
    const labeled = (...names: string[]) => names.map((name) => ({ name, color: "d73a4a", is_default: false }));
    items.value = [
      activityItem("first-comment", { item_number: 7, labels: labeled("bug") }),
      activityItem("second-comment", { item_number: 7, labels: labeled("bug") }),
      activityItem("other-item", { item_number: 8, labels: labeled("bug", "ui") }),
    ];
    render(ActivityFeed, { props: { compact: false } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: /^Labels/ }));
    const flyout = screen.getByRole("dialog", { name: "Labels" });
    expect(within(flyout).getByRole("button", { name: /^bug/ }).textContent).toContain("2");
    await fireEvent.click(within(flyout).getByRole("button", { name: /^ui/ }));

    expect(cycleLabelFilter).toHaveBeenCalledWith("ui");
  });

  it("shows a clearable author chip in the wide toolbar", async () => {
    selectedAuthor.value = "Alice";
    render(ActivityFeed, { props: { compact: false } });

    await fireEvent.click(screen.getByRole("button", { name: "Clear author filter Alice" }));

    expect(setActivityAuthor).toHaveBeenCalledWith(undefined);
  });

  it("summarizes the author in compact Filters without adding a chip row", () => {
    selectedAuthor.value = "Alice";
    render(ActivityFeed, { props: { compact: true } });

    expect(screen.getByRole("button", { name: "Filters · Alice" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Clear author filter Alice" })).toBeNull();
  });

  it("resets popover filters and author without changing the PR and issue toggles", async () => {
    selectedAuthor.value = "Alice";
    hideDrafts.value = true;
    mergeQueued.value = "exclude";
    enabledItemTypes.value = new Set(["pr"]);
    render(ActivityFeed, { props: { compact: false } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Reset filters" }));

    expect(setActivityAuthor).toHaveBeenCalledWith(undefined);
    expect(hideDrafts.value).toBe(false);
    expect(mergeQueued.value).toBeUndefined();
    expect(enabledItemTypes.value).toEqual(new Set(["pr"]));
  });

  it("exposes whether each choice and filter is on", async () => {
    render(ActivityFeed, { props: { compact: false } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));

    expect(screen.getByRole("button", { name: "Flat" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "Threaded" }).getAttribute("aria-pressed")).toBe("false");
    expect(screen.getByRole("button", { name: "7d" }).getAttribute("aria-pressed")).toBe("true");

    expect(screen.getByRole("button", { name: "Roll up commits" }).getAttribute("aria-pressed")).toBe("false");
    expect(screen.getByRole("button", { name: "Comments" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "Unassigned" }).getAttribute("aria-pressed")).toBe("false");
  });

  it("reloads server-filtered activity when state, bot, draft, or PR filters change", async () => {
    render(ActivityFeed, { props: { compact: false } });
    const mountedReads = loadActivity.mock.calls.length;

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(hideClosedMerged.value).toBe(true);
    expect(loadActivity).toHaveBeenCalledTimes(mountedReads + 1);

    const bots = screen.getByRole("button", { name: "Bots" });
    await fireEvent.click(bots);
    await fireEvent.click(bots);
    expect(hideBots.value).toBe(true);
    expect(loadActivity).toHaveBeenCalledTimes(mountedReads + 3);

    const draft = screen.getByRole("button", { name: "Draft" });
    await fireEvent.click(draft);
    expect(onlyDrafts.value).toBe(true);
    await fireEvent.click(draft);
    expect(setHideDrafts).toHaveBeenLastCalledWith(true);
    expect(loadActivity).toHaveBeenCalledTimes(mountedReads + 5);

    await fireEvent.click(screen.getByRole("button", { name: "Approved" }));
    expect(pullAttributeFilters.value).toEqual(["approved"]);
    expect(loadActivity).toHaveBeenCalledTimes(mountedReads + 6);

    await fireEvent.click(screen.getByRole("button", { name: "Passing CI" }));
    expect(pullAttributeFilters.value).toEqual(["approved", "passed_ci"]);
    await fireEvent.click(screen.getByRole("button", { name: "Failed CI" }));
    expect(pullAttributeFilters.value).toEqual(["approved", "failed_ci"]);
    await fireEvent.click(screen.getByRole("button", { name: "Failed CI" }));
    expect(pullAttributeFilters.value).toEqual(["approved"]);
    expect(loadActivity).toHaveBeenCalledTimes(mountedReads + 9);

    await fireEvent.click(screen.getByRole("button", { name: "Unassigned" }));
    expect(loadActivity).toHaveBeenCalledTimes(mountedReads + 10);

    await fireEvent.click(screen.getByRole("button", { name: "Merge queued" }));
    expect(mergeQueued.value).toBe("require");
    expect(loadActivity).toHaveBeenCalledTimes(mountedReads + 11);
  });

  it("filters loaded pull request rows by fork and workspace in the browser and leaves issues alone", async () => {
    items.value = [
      activityItem("fork", { item_number: 1, item_title: "Fork pull request", head_repo_kind: "fork" }),
      activityItem("same", {
        item_number: 2,
        item_title: "Same-repo pull request",
        head_repo_kind: "same_repo",
        workspace: { id: "ws-2", status: "ready" } as ActivityItem["workspace"],
      }),
      activityItem("issue", { item_number: 3, item_title: "Open issue", item_type: "issue" }),
    ];
    const visible = (filters: string[]) => {
      pullAttributeFilters.value = filters;
      const view = render(ActivityFeed, { props: { compact: false } });
      const text = view.container.textContent ?? "";
      view.unmount();
      return ["Fork pull request", "Same-repo pull request", "Open issue"].filter((title) => text.includes(title));
    };

    expect(visible(["fork"])).toEqual(["Fork pull request", "Open issue"]);
    expect(visible(["not_fork"])).toEqual(["Same-repo pull request", "Open issue"]);
    expect(visible(["has_workspace"])).toEqual(["Same-repo pull request", "Open issue"]);
    expect(visible(["no_workspace"])).toEqual(["Fork pull request", "Open issue"]);

    pullAttributeFilters.value = [];
    render(ActivityFeed, { props: { compact: false } });
    const mountedReads = loadActivity.mock.calls.length;
    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: "From fork" }));
    expect(pullAttributeFilters.value).toEqual(["fork"]);
    await fireEvent.click(screen.getByRole("button", { name: "Has workspace" }));
    expect(pullAttributeFilters.value).toEqual(["fork", "has_workspace"]);
    // The server applies From fork and Has workspace before its limit, so each change reloads.
    expect(loadActivity).toHaveBeenCalledTimes(mountedReads + 2);
  });

  it("keeps only closed and merged items, or only bot activity, before the next load", async () => {
    items.value = [
      activityItem("open", { item_number: 1, item_title: "Open by a person" }),
      activityItem("merged", { item_number: 2, item_title: "Merged by a person", item_state: "merged" }),
      activityItem("bot", { item_number: 3, item_title: "Open by a bot", author: "renovate[bot]" }),
    ];
    onlyClosedMerged.value = true;
    const closed = render(ActivityFeed, { props: { compact: false } });
    expect(closed.container.textContent).toContain("Merged by a person");
    expect(closed.container.textContent).not.toContain("Open by a person");
    closed.unmount();

    onlyClosedMerged.value = false;
    onlyBots.value = true;
    const bots = render(ActivityFeed, { props: { compact: false } });
    expect(bots.container.textContent).toContain("Open by a bot");
    expect(bots.container.textContent).not.toContain("Open by a person");
  });

  it("cycles Involves me, Unassigned, and Merge queued through required, excluded, and off", async () => {
    render(ActivityFeed, { props: { compact: false } });
    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));

    for (const [label, state] of [
      ["Involves me", involvesMe],
      ["Unassigned", unassigned],
      ["Merge queued", mergeQueued],
    ] as const) {
      loadActivity.mockClear();
      const row = screen.getByRole("button", { name: label });
      await fireEvent.click(row);
      expect(state.value).toBe("require");

      await fireEvent.click(row);
      expect(state.value).toBe("exclude");

      await fireEvent.click(row);
      expect(state.value).toBeUndefined();
      // Each step changes a server filter, so each one reloads.
      expect(loadActivity).toHaveBeenCalledTimes(3);
    }
  });

  it("counts an excluded filter in the badge and clears it on reset", async () => {
    unassigned.value = "exclude";
    render(ActivityFeed, { props: { compact: false } });

    const trigger = screen.getByRole("button", { name: /^Filters/ });
    expect(trigger.textContent).toContain("1");
    await fireEvent.click(trigger);
    expect(screen.getByRole("button", { name: "Unassigned , excluded" }).getAttribute("aria-pressed")).toBe("true");

    await fireEvent.click(screen.getByRole("button", { name: "Reset filters" }));
    expect(unassigned.value).toBeUndefined();
  });

  it("shows which threaded grouping is chosen", async () => {
    viewMode.value = "threaded";
    render(ActivityFeed, { props: { compact: false } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));

    expect(screen.getByRole("button", { name: "By repo" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "Flat list" }).getAttribute("aria-pressed")).toBe("false");
  });

  it("independently toggles PR and issue visibility", async () => {
    items.value = [
      activityItem("pr-comment"),
      activityItem("issue-comment", {
        item_number: 2,
        item_title: "Fix Safari issue",
        item_type: "issue",
        item_url: "https://github.com/acme/widgets/issues/2",
      }),
      branchActivityItem("branch-commit"),
    ];

    const { container } = render(ActivityFeed, { props: { compact: true } });
    const prs = screen.getByRole("switch", { name: "PRs" });
    const issues = screen.getByRole("switch", { name: "Issues" });
    expect((prs as HTMLInputElement).checked).toBe(true);
    expect((issues as HTMLInputElement).checked).toBe(true);

    await fireEvent.click(prs);
    expect([...enabledItemTypes.value]).toEqual(["issue"]);
    expect(setActivityFilterTypes).toHaveBeenLastCalledWith([
      "new_issue",
      "default_branch_commit",
      "default_branch_force_push",
      "comment",
      "review",
      "force_push",
      "notification",
    ]);

    await fireEvent.click(issues);
    expect(enabledItemTypes.value.size).toBe(0);
    expect(container.textContent).toContain("Refresh cache warmer");
  });

  it("hides the complete PR thread when PRs are disabled", async () => {
    viewMode.value = "threaded";
    enabledItemTypes.value = new Set(["issue"]);
    items.value = [
      activityItem("pr-comment"),
      activityItem("pr-review", {
        activity_type: "review",
        body_preview: "Approved",
      }),
      activityItem("issue-comment", {
        item_number: 2,
        item_title: "Fix Safari issue",
        item_type: "issue",
        item_url: "https://github.com/acme/widgets/issues/2",
      }),
      branchActivityItem("branch-commit"),
    ];

    const { container } = render(ActivityFeed, { props: { compact: true } });
    expect(container.textContent).not.toContain("Add widget caching layer");
    expect(container.textContent).not.toContain("Approved");
    expect(container.textContent).toContain("Fix Safari issue");
    expect(container.textContent).toContain("Refresh cache warmer");
  });

  it("respects hide org name in compact flat rows", () => {
    hideOrgName.value = true;

    const { container } = render(ActivityFeed, {
      props: { compact: true },
    });

    const repoLabels = Array.from(container.querySelectorAll(".compact-meta > span:first-child")).map((el) =>
      el.textContent?.trim(),
    );
    expect(repoLabels).toEqual(["widgets", "widgets"]);
    expect(container.textContent).not.toContain("acme/widgets");
  });

  it("respects hide org name in table flat rows", () => {
    hideOrgName.value = true;

    const { container } = render(ActivityFeed, {
      props: { compact: false },
    });

    const repoCells = Array.from(container.querySelectorAll(".activity-row .col-repo")).map((el) =>
      el.textContent?.trim(),
    );
    expect(repoCells).toEqual(["widgets", "widgets"]);
    expect(container.textContent).not.toContain("acme/widgets");
  });

  it("keeps hidden-org flat activity repo labels distinguishable", () => {
    hideOrgName.value = true;
    items.value = [
      activityItem("acme-widgets"),
      activityItem("platform-widgets", {
        id: "platform-widgets",
        item_number: 2,
        repo_owner: "platform",
        repo_name: "widgets",
        repo: {
          provider: "gitlab",
          platform_host: "gitlab.example.com",
          owner: "platform",
          name: "widgets",
          repo_path: "platform/widgets",
        },
      }),
    ];

    const { container } = render(ActivityFeed, {
      props: { compact: false },
    });

    const repoCells = Array.from(container.querySelectorAll(".activity-row .col-repo")).map((el) =>
      el.textContent?.trim(),
    );
    expect(repoCells).toEqual(["acme/widgets", "platform/widgets"]);
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

    expect(container.querySelectorAll(".activity-compact-row.selected")).toHaveLength(2);
  });

  it("hides the collapse-all control in flat mode", () => {
    render(ActivityFeed, { props: { compact: true } });
    expect(screen.queryByRole("button", { name: /Collapse all|Expand all/ })).toBeNull();
  });

  it("shows workspace indicators in flat activity rows", () => {
    items.value = [
      activityItem("pr-workspace", {
        workspace: { id: "ws-pr-1", status: "ready" },
      }),
      activityItem("issue-workspace", {
        item_number: 2,
        item_title: "Track workspace setup",
        item_type: "issue",
        item_url: "https://github.com/acme/widgets/issues/2",
        workspace: { id: "ws-issue-2", status: "creating" },
      }),
    ];

    render(ActivityFeed, {
      props: { compact: false },
    });

    expect(screen.getByLabelText("Workspace attached (ready)")).toBeTruthy();
    expect(screen.getByLabelText("Workspace attached (creating)")).toBeTruthy();
  });

  it("renders branch commits in compact rows without a fake item number", () => {
    items.value = [branchActivityItem("branch-commit")];

    const { container } = render(ActivityFeed, {
      props: { compact: true },
    });

    const row = container.querySelector(".activity-compact-row");
    expect(row?.textContent).toContain("Refresh cache warmer");
    expect(row?.textContent).toContain("main");
    expect(row?.textContent).toContain("a1b2c3d");
    expect(row?.textContent).not.toContain("#0");
    expect(row?.querySelector(".chip--kind-pr")).toBeNull();
    expect(row?.querySelector(".chip--kind-issue")).toBeNull();
  });

  it("shows each default-branch commit with its linked author in the flat table", () => {
    items.value = [
      branchActivityItem("branch-commit-1", {
        body_preview: "Ship direct main commit 1",
        commit_sha: "1111111111111111111111111111111111111111",
      }),
      branchActivityItem("branch-commit-2", {
        body_preview: "Ship direct main commit 2",
        commit_sha: "2222222222222222222222222222222222222222",
      }),
      branchActivityItem("branch-commit-3", {
        body_preview: "Ship direct main commit 3",
        commit_sha: "3333333333333333333333333333333333333333",
      }),
    ];

    const { container } = render(ActivityFeed, {
      props: { compact: false },
    });

    const rows = container.querySelectorAll(".activity-row");
    expect(rows).toHaveLength(3);
    expect(rows[0]?.querySelector(".cell--author")?.textContent?.trim()).toBe("alice");
    expect(container.textContent).toContain("Ship direct main commit 1");
    expect(container.textContent).toContain("Ship direct main commit 2");
    expect(container.textContent).toContain("Ship direct main commit 3");
    expect(container.textContent).not.toContain("3 commits");
  });

  it("rolls up default-branch commits in the flat table when enabled", () => {
    rollUpCommits.value = true;
    items.value = [
      branchActivityItem("branch-commit-1", {
        body_preview: "Ship direct main commit 1",
        commit_sha: "1111111111111111111111111111111111111111",
      }),
      branchActivityItem("branch-commit-2", {
        body_preview: "Ship direct main commit 2",
        commit_sha: "2222222222222222222222222222222222222222",
      }),
      branchActivityItem("branch-commit-3", {
        body_preview: "Ship direct main commit 3",
        commit_sha: "3333333333333333333333333333333333333333",
      }),
    ];

    const { container } = render(ActivityFeed, {
      props: { compact: false },
    });

    const rows = container.querySelectorAll(".activity-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.classList.contains("collapsed-row")).toBe(true);
    expect(rows[0]?.textContent).toContain("3 commits");
    expect(container.textContent).not.toContain("Ship direct main commit 1");
    expect(container.textContent).not.toContain("Ship direct main commit 2");
    expect(container.textContent).not.toContain("Ship direct main commit 3");
  });

  it("keeps consecutive comments expanded in the flat table", () => {
    items.value = [
      activityItem("comment-1", {
        body_preview: "First comment",
        created_at: "2026-04-27T12:03:00Z",
      }),
      activityItem("comment-2", {
        body_preview: "Second comment",
        created_at: "2026-04-27T12:02:00Z",
      }),
      activityItem("comment-3", {
        body_preview: "Third comment",
        created_at: "2026-04-27T12:01:00Z",
      }),
    ];

    const { container } = render(ActivityFeed, {
      props: { compact: false },
    });

    const rows = container.querySelectorAll(".activity-row");
    expect(rows).toHaveLength(3);
    expect(container.querySelectorAll(".activity-row.collapsed-row")).toHaveLength(0);
    expect(container.textContent).not.toContain("3 commits");
  });

  it("renders default-branch force-pushes in table rows", () => {
    items.value = [
      branchActivityItem("force-push", {
        activity_type: "default_branch_force_push",
        after_sha: "def5678901234567890123456789012345678901",
        author: "kenn-forge",
        author_name: "",
        before_sha: "abc1234901234567890123456789012345678901",
        body_preview: "abc1234901234567890123456789012345678901 -> def5678901234567890123456789012345678901",
        commit_sha: "",
        activity_url: "",
      }),
    ];

    const { container } = render(ActivityFeed, {
      props: { compact: false },
    });

    const row = container.querySelector(".activity-row");
    expect(row?.textContent).toContain("Force-pushed");
    expect(row?.textContent).toContain("abc1234 -> def5678");
    expect(row?.textContent).toContain("main");
    expect(row?.textContent).not.toContain("#0");
    expect(row?.querySelector(".chip--kind-pr")).toBeNull();
    expect(row?.querySelector(".chip--kind-issue")).toBeNull();
  });

  it("deselecting Commits hides default-branch commits but keeps PR commits", async () => {
    render(ActivityFeed, { props: { compact: true } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Commits" }));

    expect(setActivityFilterTypes).toHaveBeenCalledWith([
      "new_pr",
      "new_issue",
      "default_branch_force_push",
      "comment",
      "review",
      "commit",
      "force_push",
      "notification",
    ]);
  });

  it("deselecting Force pushes also hides default-branch force pushes", async () => {
    render(ActivityFeed, { props: { compact: true } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Force pushes" }));

    expect(setActivityFilterTypes).toHaveBeenCalledWith([
      "new_pr",
      "new_issue",
      "default_branch_commit",
      "comment",
      "review",
      "commit",
      "notification",
    ]);
  });

  it("can hide default-branch activity from the filter dropdown", async () => {
    render(ActivityFeed, { props: { compact: true } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(
      screen.getByRole("button", {
        name: "Hide default-branch activity",
      }),
    );

    expect(hideDefaultBranchActivity.value).toBe(true);
    expect(setActivityFilterTypes).toHaveBeenCalledWith([
      "new_pr",
      "new_issue",
      "comment",
      "review",
      "commit",
      "force_push",
      "notification",
    ]);
  });

  it("deselecting Notifications drops the notification type from the request", async () => {
    render(ActivityFeed, { props: { compact: true } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Notifications" }));

    expect(showNotifications.value).toBe(false);
    expect(setActivityFilterTypes).toHaveBeenCalledWith([
      "new_pr",
      "new_issue",
      "default_branch_commit",
      "default_branch_force_push",
      "comment",
      "review",
      "commit",
      "force_push",
    ]);
  });

  it("can enable commit roll-up from the view dropdown", async () => {
    render(ActivityFeed, { props: { compact: true } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Roll up commits" }));

    expect(rollUpCommits.value).toBe(true);
  });

  it("can deselect the last remaining event type", async () => {
    enabledEvents.value = new Set(["comment"]);
    render(ActivityFeed, { props: { compact: true } });

    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Comments" }));

    // Removing the last top-level event keeps PR timeline commits and
    // notifications without leaking PR/issue opening rows.
    expect(enabledEvents.value.size).toBe(0);
    expect(setActivityFilterTypes).toHaveBeenCalledWith(["commit", "notification"]);
  });

  it("hides notifications on merged PRs even in a notifications-only feed", () => {
    hideClosedMerged.value = true;
    // Notifications-only: no sibling PR rows are present, so the filter must
    // read each notification's own subject_state (its linked PR/issue's
    // state) rather than item_state, which holds unread/read.
    items.value = [
      activityItem("ntf:1", {
        activity_type: "notification",
        item_state: "unread",
        subject_state: "merged",
        item_number: 1,
        item_title: "Merged work",
        body_preview: "review_requested",
      }),
      activityItem("ntf:2", {
        activity_type: "notification",
        item_state: "unread",
        subject_state: "open",
        item_number: 2,
        item_title: "Open work",
        item_url: "https://github.com/acme/widgets/pull/2",
        body_preview: "mention",
      }),
    ];
    const { container } = render(ActivityFeed, { props: { compact: true } });
    expect(container.textContent).toContain("Open work");
    expect(container.textContent).not.toContain("Merged work");
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

describe("ActivityFeed notification rows", () => {
  beforeEach(() => {
    viewMode.value = "flat";
    showNotifications.value = true;
    markNotificationSeen.mockClear();
  });

  afterEach(() => {
    cleanup();
    items.value = [];
  });

  it("offers Mark seen only on unread notification rows and calls the store", async () => {
    items.value = [
      activityItem("ntf:42", {
        activity_type: "notification",
        item_state: "unread",
        body_preview: "review_requested",
      }),
      activityItem("comment", { activity_type: "comment" }),
    ];

    render(ActivityFeed, { props: {} });

    expect(screen.getByText("Review requested")).toBeTruthy();
    const buttons = screen.getAllByRole("button", { name: "Mark notification seen" });
    expect(buttons).toHaveLength(1);

    await fireEvent.click(buttons[0]!);
    expect(markNotificationSeen).toHaveBeenCalledTimes(1);
    expect(markNotificationSeen.mock.calls[0]![0]).toMatchObject({ id: "ntf:42" });
  });

  it("hides Mark seen once a notification row is read", () => {
    items.value = [
      activityItem("ntf:42", {
        activity_type: "notification",
        item_state: "read",
        body_preview: "mention",
      }),
    ];

    render(ActivityFeed, { props: {} });

    expect(screen.getByText("Mentioned")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Mark notification seen" })).toBeNull();
  });

  it("offers Mark seen on compact unread notification rows and calls the store", async () => {
    items.value = [
      activityItem("ntf:42", {
        activity_type: "notification",
        item_state: "unread",
        body_preview: "review_requested",
      }),
    ];

    const { container } = render(ActivityFeed, { props: { compact: true } });

    // The row body stays a real <button> so keyboard users can focus and
    // activate it; the mark-seen control is a separate, non-nested button.
    const rowBody = container.querySelector(".activity-compact-row");
    expect(rowBody?.tagName).toBe("BUTTON");

    const btn = screen.getByRole("button", { name: "Mark notification seen" });
    expect(rowBody?.contains(btn)).toBe(false);

    await fireEvent.click(btn);
    expect(markNotificationSeen).toHaveBeenCalledTimes(1);
    expect(markNotificationSeen.mock.calls[0]![0]).toMatchObject({ id: "ntf:42" });
  });
});
