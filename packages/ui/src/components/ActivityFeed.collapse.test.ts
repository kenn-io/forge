import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import ActivityFeed from "./ActivityFeed.svelte";

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
      getEnabledEvents: () => new Set(["comment", "review", "commit", "force_push"]),
      getHideClosedMerged: () => false,
      getHideBots: () => false,
      getItemFilter: () => "all",
      getActivityItems: () => [],
      getActivityError: () => null,
      getViewMode: () => "threaded",
      getTimeRange: () => "7d",
      isActivityLoading: () => false,
      isActivityCapped: () => false,
      getCollapseThreads: () => false,
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
    sync: { subscribeSyncComplete: vi.fn(() => () => undefined) },
    grouping: { getGroupByRepo: () => true, setGroupByRepo: vi.fn() },
  }),
}));

describe("ActivityFeed collapse-all control", () => {
  afterEach(() => {
    cleanup();
    collapseAllThreads.mockClear();
    expandAllThreads.mockClear();
  });

  it("renders a Collapse all button in threaded mode and wires it", async () => {
    render(ActivityFeed, { props: {} });
    const btn = screen.getByRole("button", { name: "Collapse all" });
    await fireEvent.click(btn);
    expect(collapseAllThreads).toHaveBeenCalledTimes(1);
    expect(expandAllThreads).not.toHaveBeenCalled();
  });
});
