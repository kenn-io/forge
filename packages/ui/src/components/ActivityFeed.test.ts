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

const items = vi.hoisted(() => ({ value: [] as ActivityItem[] }));
const viewMode = vi.hoisted(() => ({ value: "flat" as "flat" | "threaded" }));
const collapseThreads = vi.hoisted(() => ({ value: false }));
const collapseAllThreads = vi.hoisted(() => vi.fn());
const expandAllThreads = vi.hoisted(() => vi.fn());
const hideDefaultBranchActivity = vi.hoisted(() => ({ value: false }));
const hideOrgName = vi.hoisted(() => ({ value: false }));
const setActivityFilterTypes = vi.hoisted(() => vi.fn());

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
      getHideDefaultBranchActivity: () => hideDefaultBranchActivity.value,
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
      setActivityFilterTypes,
      setItemFilter: vi.fn(),
      setEnabledEvents: vi.fn(),
      setHideClosedMerged: vi.fn(),
      setHideBots: vi.fn(),
      setHideDefaultBranchActivity: vi.fn((value: boolean) => {
        hideDefaultBranchActivity.value = value;
      }),
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
    hideDefaultBranchActivity.value = false;
    hideOrgName.value = false;
    setActivityFilterTypes.mockClear();
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

  it("respects hide org name in compact flat rows", () => {
    hideOrgName.value = true;

    const { container } = render(ActivityFeed, {
      props: { compact: true },
    });

    const repoLabels = Array.from(
      container.querySelectorAll(".compact-meta > span:first-child"),
    ).map((el) => el.textContent?.trim());
    expect(repoLabels).toEqual(["widgets", "widgets"]);
    expect(container.textContent).not.toContain("acme/widgets");
  });

  it("respects hide org name in table flat rows", () => {
    hideOrgName.value = true;

    const { container } = render(ActivityFeed, {
      props: { compact: false },
    });

    const repoCells = Array.from(
      container.querySelectorAll(".activity-row .col-repo"),
    ).map((el) => el.textContent?.trim());
    expect(repoCells).toEqual(["widgets", "widgets"]);
    expect(container.textContent).not.toContain("acme/widgets");
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

  it("shows individual default-branch commits in the flat table", () => {
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
    expect(container.textContent).toContain("Ship direct main commit 1");
    expect(container.textContent).toContain("Ship direct main commit 2");
    expect(container.textContent).toContain("Ship direct main commit 3");
    expect(container.textContent).not.toContain("3 commits");
  });

  it("renders default-branch force-pushes in table rows", () => {
    items.value = [
      branchActivityItem("force-push", {
        activity_type: "default_branch_force_push",
        after_sha: "def5678901234567890123456789012345678901",
        author: "middleman",
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

  it("can hide default-branch activity from the filter dropdown", async () => {
    render(ActivityFeed, { props: { compact: true } });

    await fireEvent.click(screen.getByRole("button", { name: "View" }));
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
    ]);
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
