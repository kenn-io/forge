import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { SvelteMap } from "svelte/reactivity";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { ActivityItem } from "../api/types.js";
import { createGroupingStore, type GroupingStore } from "../stores/grouping.svelte.js";
import ActivityFeed from "./ActivityFeed.svelte";

// Component conversion of the activity-side cases from
// frontend/tests/e2e-full/grouping-toggle.spec.ts. These use the REAL
// grouping store (localStorage-persisted), so the grouping/hide-org state
// set elsewhere in the app (PR list) honestly drives the activity views,
// and toggling "Hide org name" through the View dropdown reactively
// updates the rendered rows.

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
const groupingRef = vi.hoisted(() => ({
  store: null as GroupingStore | null,
}));
// Reactive view-mode backing so clicking "Threaded" in the dropdown updates
// the rendered view like the real activity store would.
const viewModeRef = vi.hoisted(() => ({
  map: null as Map<string, "flat" | "threaded"> | null,
}));

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
      getViewMode: () => viewModeRef.map!.get("viewMode")!,
      getTimeRange: () => "7d",
      isActivityLoading: () => false,
      isActivityCapped: () => false,
      getCollapseThreads: () => false,
      collapseAllThreads: vi.fn(),
      expandAllThreads: vi.fn(),
      isThreadItemExpanded: () => true,
      toggleThreadItem: vi.fn(),
      setActivityFilterTypes: vi.fn(),
      setItemFilter: vi.fn(),
      setEnabledEvents: vi.fn(),
      setHideClosedMerged: vi.fn(),
      setHideBots: vi.fn(),
      setHideDefaultBranchActivity: vi.fn(),
      setActivitySearch: vi.fn(),
      setTimeRange: vi.fn(),
      setViewMode: vi.fn((mode: "flat" | "threaded") => {
        viewModeRef.map!.set("viewMode", mode);
      }),
      syncToURL: vi.fn(),
    },
    settings: {
      isSettingsLoaded: () => true,
      hasConfiguredRepos: () => true,
    },
    sync: {
      subscribeSyncComplete: vi.fn(() => () => undefined),
    },
    grouping: groupingRef.store!,
  }),
}));

async function openViewDropdown(): Promise<void> {
  await fireEvent.click(screen.getByRole("button", { name: "View" }));
}

function ungroupedStore(): GroupingStore {
  // The PR list persists ungrouped mode under this key; constructing the
  // store afterwards mirrors the cross-view sync the e2e test exercised.
  localStorage.setItem("middleman:groupingMode", "flat");
  return createGroupingStore();
}

describe("ActivityFeed grouping toggle", () => {
  beforeEach(() => {
    localStorage.removeItem("middleman:groupingMode");
    localStorage.removeItem("middleman:groupByRepo");
    localStorage.removeItem("middleman:hideOrgName");
    viewModeRef.map = new SvelteMap<string, "flat" | "threaded">([["viewMode", "flat"]]);
    groupingRef.store = createGroupingStore();
    items.value = [
      // Latest event is bob's review; the PR itself was authored by alice
      // (item_author rides on every event row, as the API delivers it).
      activityItem("widgets-pr1-review", {
        activity_type: "review",
        author: "bob",
        created_at: "2026-04-27T13:00:00Z",
      }),
      activityItem("widgets-pr1-opened", {
        activity_type: "new_pr",
        created_at: "2026-04-27T12:00:00Z",
      }),
    ];
  });

  afterEach(() => {
    cleanup();
  });

  it("persisted ungrouped mode syncs to the threaded view", () => {
    groupingRef.store = ungroupedStore();
    viewModeRef.map!.set("viewMode", "threaded");

    const { container } = render(ActivityFeed, { props: {} });

    expect(container.querySelector(".threaded-view")).not.toBeNull();
    // No repo headers in ungrouped mode; repo tags appear on item rows.
    expect(container.querySelector(".threaded-view .repo-header")).toBeNull();
    expect(container.querySelector(".threaded-view .item-row .repo-chip.repo-tag")).not.toBeNull();
  });

  it("hides the By repo grouping in flat mode and offers it in threaded", async () => {
    render(ActivityFeed, { props: {} });

    await openViewDropdown();
    // Dropdown is open (view-mode items render) but no grouping section.
    expect(screen.getByRole("button", { name: "Threaded" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "By repo" })).toBeNull();

    // Switch to threaded through the dropdown itself.
    await fireEvent.click(screen.getByRole("button", { name: "Threaded" }));

    expect(screen.getByRole("button", { name: "By repo" })).toBeTruthy();
  });

  it("flat table rows respect hide org name toggled through the dropdown", async () => {
    const { container } = render(ActivityFeed, { props: { compact: false } });

    const repoCell = container.querySelector(".activity-row .col-repo");
    expect(repoCell?.textContent?.trim()).toBe("acme/widgets");

    await openViewDropdown();
    await fireEvent.click(screen.getByRole("button", { name: "Hide org name" }));

    const repoCellAfter = container.querySelector(".activity-row .col-repo");
    expect(repoCellAfter?.textContent?.trim()).toBe("widgets");
    expect(container.querySelector(".activity-table")?.textContent).not.toContain("acme/widgets");
    // Persisted, so the PR/issue lists pick up the same setting.
    expect(localStorage.getItem("middleman:hideOrgName")).toBe("1");
  });

  it("threaded grouped headers respect hide org name", async () => {
    viewModeRef.map!.set("viewMode", "threaded");

    const { container } = render(ActivityFeed, { props: {} });

    const header = container.querySelector(".threaded-view .repo-header .repo-name");
    expect(header?.textContent).toBe("acme/widgets");

    await openViewDropdown();
    await fireEvent.click(screen.getByRole("button", { name: "Hide org name" }));

    const headerAfter = container.querySelector(".threaded-view .repo-header .repo-name");
    expect(headerAfter?.textContent).toBe("widgets");
    expect(container.querySelector(".threaded-view")?.textContent).not.toContain("acme/widgets");
  });

  it("threaded ungrouped rows show the item author and hide org repo chips", async () => {
    groupingRef.store = ungroupedStore();
    viewModeRef.map!.set("viewMode", "threaded");

    const { container } = render(ActivityFeed, { props: {} });

    const row = container.querySelector(".threaded-view .item-row:not(.branch-activity-row)");
    expect(row).not.toBeNull();
    // The thread row attributes the item to its author (alice), not the
    // latest actor (bob, whose review is the most recent event).
    expect(row!.querySelector(".cell--author")?.textContent?.trim()).toBe("alice");
    expect(row!.querySelector(".repo-chip__label")?.textContent).toBe("acme/widgets");

    await openViewDropdown();
    await fireEvent.click(screen.getByRole("button", { name: "Hide org name" }));

    const rowAfter = container.querySelector(".threaded-view .item-row:not(.branch-activity-row)");
    expect(rowAfter!.querySelector(".repo-chip__label")?.textContent).toBe("widgets");
    expect(container.querySelector(".threaded-view")?.textContent).not.toContain("acme/widgets");
  });
});
