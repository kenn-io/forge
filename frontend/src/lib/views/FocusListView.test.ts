import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import FocusListView from "./FocusListViewRuntimeHarness.svelte";
import {
  getIssueInvolvesMe,
  getIssueReferencedByPR,
  getIssueSearch,
  getPullInvolvesMe,
  getPullSearch,
  resetFocusListViewState,
  setIssueInvolvesMe,
  setIssueReferencedByPR,
  setIssueSearch,
  setPullInvolvesMe,
  setPullSearch,
} from "../../test/focusListViewState.svelte.js";

const pullSearch = vi.hoisted(() => vi.fn());
const issueSearch = vi.hoisted(() => vi.fn());
const loadPulls = vi.hoisted(() => vi.fn());
const loadIssues = vi.hoisted(() => vi.fn());
const setPullsInvolvesMe = vi.hoisted(() => vi.fn());
const setIssuesInvolvesMe = vi.hoisted(() => vi.fn());
const setIssuesReferencedByPR = vi.hoisted(() => vi.fn());
const unsubscribeSync = vi.hoisted(() => vi.fn());
const subscribeSyncComplete = vi.hoisted(() => vi.fn(() => unsubscribeSync));
vi.mock("../context.js", () => ({
  getActions: () => ({ importItem: vi.fn() }),
  getNavigate: () => vi.fn(),
  getStores: () => ({
    grouping: {
      getGroupingMode: () => "flat",
      getHideOrgName: () => false,
      setGroupingMode: vi.fn(),
    },
    issues: {
      getInvolvesMe: getIssueInvolvesMe,
      getReferencedByPR: getIssueReferencedByPR,
      canFilterReferencedByPR: () => true,
      getIssueSearchQuery: getIssueSearch,
      getHideBots: () => false,
      getIssueFilterState: () => "open",
      getIssues: () => [],
      getIssuesError: () => null,
      isIssuesLoading: () => false,
      loadIssues,
      setHideBots: vi.fn(),
      setInvolvesMe: (value: boolean) => {
        setIssuesInvolvesMe(value);
        setIssueInvolvesMe(value);
      },
      setReferencedByPR: (value: boolean) => {
        setIssuesReferencedByPR(value);
        setIssueReferencedByPR(value);
      },
      setIssueFilterState: vi.fn(),
      setIssueSearchQuery: (value: string | undefined) => {
        issueSearch(value);
        setIssueSearch(value);
      },
    },
    pulls: {
      getInvolvesMe: getPullInvolvesMe,
      getSearchQuery: getPullSearch,
      getError: () => null,
      getFilterState: () => "open",
      getPulls: () => [],
      isLoading: () => false,
      loadPulls,
      setFilterState: vi.fn(),
      setInvolvesMe: (value: boolean) => {
        setPullsInvolvesMe(value);
        setPullInvolvesMe(value);
      },
      setSearchQuery: (value: string | undefined) => {
        pullSearch(value);
        setPullSearch(value);
      },
    },
    settings: {
      hasConfiguredRepos: () => true,
      isSettingsLoaded: () => true,
    },
    sync: {
      getSyncState: () => ({ last_run_at: "2026-08-04T00:00:00Z", running: false }),
      subscribeSyncComplete,
    },
  }),
}));

describe("FocusListView search", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    pullSearch.mockClear();
    issueSearch.mockClear();
    loadPulls.mockClear();
    loadIssues.mockClear();
    setPullsInvolvesMe.mockClear();
    setIssuesInvolvesMe.mockClear();
    setIssuesReferencedByPR.mockClear();
    unsubscribeSync.mockClear();
    subscribeSyncComplete.mockClear();
    resetFocusListViewState();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("cancels a pending search when the retained view changes identity", async () => {
    const view = render(FocusListView, { props: { listType: "mrs", repo: "acme/one" } });
    await fireEvent.input(screen.getByLabelText("Search PRs"), { target: { value: "stale" } });

    await view.rerender({ listType: "issues", repo: "acme/two" });
    await vi.advanceTimersByTimeAsync(300);

    expect(screen.getByLabelText<HTMLInputElement>("Search issues").value).toBe("");
    expect(pullSearch).not.toHaveBeenCalled();
    expect(issueSearch).not.toHaveBeenCalled();
    expect(unsubscribeSync).toHaveBeenCalledTimes(1);

    view.unmount();
    expect(unsubscribeSync).toHaveBeenCalledTimes(2);
  });

  it("retains each list type's stored query when repository identity changes", async () => {
    setPullSearch("owned by me");
    const view = render(FocusListView, { props: { listType: "mrs", repo: "acme/one" } });

    expect(screen.getByLabelText<HTMLInputElement>("Search PRs").value).toBe("owned by me");
    await view.rerender({ listType: "mrs", repo: "acme/two" });

    expect(screen.getByLabelText<HTMLInputElement>("Search PRs").value).toBe("owned by me");
  });

  it("publishes one debounced search without restarting polling ownership", async () => {
    const view = render(FocusListView, { props: { listType: "mrs", repo: "acme/one" } });
    expect(loadPulls).toHaveBeenCalledTimes(1);
    expect(subscribeSyncComplete).toHaveBeenCalledTimes(1);

    await fireEvent.input(screen.getByLabelText("Search PRs"), { target: { value: "owned" } });
    await vi.advanceTimersByTimeAsync(300);

    expect(pullSearch).toHaveBeenCalledWith("owned");
    expect(loadPulls).toHaveBeenCalledTimes(2);
    expect(subscribeSyncComplete).toHaveBeenCalledTimes(1);
    expect(unsubscribeSync).not.toHaveBeenCalled();
    view.unmount();
  });

  it.each([
    ["mrs" as const, setPullsInvolvesMe, loadPulls],
    ["issues" as const, setIssuesInvolvesMe, loadIssues],
  ])("uses the shared Involves me control for %s", async (listType, setInvolvesMe, loadList) => {
    render(FocusListView, { props: { listType, repo: "acme/one" } });
    loadList.mockClear();

    const control = screen.getByRole("button", { name: "Involves me" });
    expect(control.getAttribute("aria-pressed")).toBe("false");
    await fireEvent.click(control);

    expect(setInvolvesMe).toHaveBeenCalledWith(true);
    expect(loadList).toHaveBeenCalledTimes(1);
    expect(control.getAttribute("aria-pressed")).toBe("true");
  });

  it("filters issues by pull request references", async () => {
    render(FocusListView, { props: { listType: "issues", repo: "acme/one" } });
    loadIssues.mockClear();

    const control = screen.getByRole("button", { name: "Referenced by PR" });
    expect(control.getAttribute("aria-pressed")).toBe("false");
    await fireEvent.click(control);

    expect(setIssuesReferencedByPR).toHaveBeenCalledWith(true);
    expect(loadIssues).toHaveBeenCalledTimes(1);
    expect(control.getAttribute("aria-pressed")).toBe("true");
  });
});
