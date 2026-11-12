import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { SvelteMap } from "svelte/reactivity";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import FocusListView from "./FocusListViewRuntimeHarness.svelte";
import {
  getIssueInvolvesMe,
  getIssueReferencedByPR,
  getIssueSearch,
  getIssueUnassigned,
  getPullInvolvesMe,
  getPullSearch,
  getPullUnassigned,
  resetFocusListViewState,
  setIssueInvolvesMe,
  setIssueReferencedByPR,
  setIssueSearch,
  setIssueUnassigned,
  setPullInvolvesMe,
  setPullSearch,
  setPullUnassigned,
} from "../../test/focusListViewState.svelte.js";
import { getGlobalRepo, setGlobalRepo } from "../stores/filter.svelte.js";
import type { TriFilter } from "../utils/tri-filter.js";

const pullSearch = vi.hoisted(() => vi.fn());
const issueSearch = vi.hoisted(() => vi.fn());
const loadPulls = vi.hoisted(() => vi.fn());
const loadMorePulls = vi.hoisted(() => vi.fn());
const loadIssues = vi.hoisted(() => vi.fn());
const loadMoreIssues = vi.hoisted(() => vi.fn());
const setPullsInvolvesMe = vi.hoisted(() => vi.fn());
const setIssuesInvolvesMe = vi.hoisted(() => vi.fn());
const setPullsUnassigned = vi.hoisted(() => vi.fn());
const setIssuesUnassigned = vi.hoisted(() => vi.fn());
const setIssuesReferencedByPR = vi.hoisted(() => vi.fn());
const unsubscribeSync = vi.hoisted(() => vi.fn());
const subscribeSyncComplete = vi.hoisted(() => vi.fn(() => unsubscribeSync));
const pullListCapped = vi.hoisted(() => ({ state: undefined as SvelteMap<string, boolean> | undefined }));
const issueListCapped = vi.hoisted(() => ({ state: undefined as SvelteMap<string, boolean> | undefined }));
vi.mock("../context.js", () => ({
  getActions: () => ({ importItem: vi.fn() }),
  getNavigate: () => vi.fn(),
  getStores: () => ({
    collapsedRepos: { isCollapsed: () => false, toggle: vi.fn() },
    grouping: {
      getGroupingMode: () => "flat",
      getGroupByRepo: () => false,
      getHideOrgName: () => false,
      setGroupingMode: vi.fn(),
      setGroupByRepo: vi.fn(),
      setHideOrgName: vi.fn(),
    },
    issues: {
      getInvolvesMe: getIssueInvolvesMe,
      getUnassigned: getIssueUnassigned,
      getReferencedByPR: getIssueReferencedByPR,
      canFilterReferencedByPR: () => true,
      getIssueSearchQuery: getIssueSearch,
      getHideBots: () => false,
      getIssueFilterState: () => "open",
      getIssues: () => [],
      getFilteredIssues: () => [],
      issuesByRepo: () => new Map(),
      getLabelFilters: () => new Map(),
      clearLabelFilters: vi.fn(),
      cycleLabelFilter: vi.fn(),
      getIssueFilterStarred: () => false,
      setIssueFilterStarred: vi.fn(),
      getIssuesError: () => null,
      isIssuesLoading: () => false,
      isIssueListCapped: () => issueListCapped.state?.get("capped") ?? false,
      loadIssues,
      loadMoreIssues,
      searchTextChanged: vi.fn(),
      setHideBots: vi.fn(),
      setInvolvesMe: (value: TriFilter) => {
        setIssuesInvolvesMe(value);
        setIssueInvolvesMe(value);
      },
      setUnassigned: (value: TriFilter) => {
        setIssuesUnassigned(value);
        setIssueUnassigned(value);
      },
      setReferencedByPR: (value: TriFilter) => {
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
      getUnassigned: getPullUnassigned,
      getSearchQuery: getPullSearch,
      getError: () => null,
      getFilterState: () => "open",
      getPulls: () => [],
      getFilteredPulls: () => [],
      pullsByRepo: () => new Map(),
      getSidebarRows: () => [],
      getAttributeFilters: () => [],
      getLabelFilters: () => new Map(),
      getLocalFilterCount: () => 0,
      getFilterStarred: () => false,
      setFilterStarred: vi.fn(),
      getStackTree: () => false,
      setStackTree: vi.fn(),
      clearLocalFilters: vi.fn(),
      cycleLabelFilter: vi.fn(),
      cycleAttributeFilter: vi.fn(),
      toggleAttributeFilter: vi.fn(),
      isLoading: () => false,
      isListCapped: () => pullListCapped.state?.get("capped") ?? false,
      loadPulls,
      loadMorePulls,
      searchTextChanged: vi.fn(),
      setFilterState: vi.fn(),
      setInvolvesMe: (value: TriFilter) => {
        setPullsInvolvesMe(value);
        setPullInvolvesMe(value);
      },
      setUnassigned: (value: TriFilter) => {
        setPullsUnassigned(value);
        setPullUnassigned(value);
      },
      setSearchQuery: (value: string | undefined) => {
        pullSearch(value);
        setPullSearch(value);
      },
    },
    settings: {
      getConfiguredRepos: () => [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
          platform_repo_id: 1101,
          is_glob: false,
          matched_repo_count: 1,
          hidden_from_ui: false,
        },
      ],
      getRepoPresets: () => [
        {
          name: "Backend",
          repos: [
            {
              provider: "github",
              platform_host: "github.com",
              platform_repo_id: 1101,
              repo_path: "acme/api",
            },
          ],
        },
      ],
      hasConfiguredRepos: () => true,
      isSettingsLoaded: () => true,
    },
    sync: {
      getSyncState: () => ({ last_run_at: "2026-08-04T00:00:00Z", running: false }),
      subscribeSyncCompleteWithoutLiveUpdates: subscribeSyncComplete,
    },
  }),
}));

describe("FocusListView search", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    pullSearch.mockClear();
    issueSearch.mockClear();
    loadPulls.mockClear();
    loadMorePulls.mockClear();
    loadIssues.mockClear();
    loadMoreIssues.mockClear();
    setPullsInvolvesMe.mockClear();
    setIssuesInvolvesMe.mockClear();
    setPullsUnassigned.mockClear();
    setIssuesUnassigned.mockClear();
    setIssuesReferencedByPR.mockClear();
    unsubscribeSync.mockClear();
    subscribeSyncComplete.mockClear();
    resetFocusListViewState();
    pullListCapped.state = new SvelteMap([["capped", false]]);
    issueListCapped.state = new SvelteMap([["capped", false]]);
    setGlobalRepo(undefined);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
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

  it("stops polling while the document is hidden and refreshes at once when it is shown", async () => {
    let visibilityState: DocumentVisibilityState = "visible";
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => visibilityState,
    });
    const setVisibility = (state: DocumentVisibilityState) => {
      visibilityState = state;
      document.dispatchEvent(new Event("visibilitychange"));
    };
    try {
      const view = render(FocusListView, { props: { listType: "mrs", repo: "acme/one" } });
      expect(loadPulls).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(15_000);
      expect(loadPulls).toHaveBeenCalledTimes(2);

      setVisibility("hidden");
      await vi.advanceTimersByTimeAsync(60_000);
      expect(loadPulls).toHaveBeenCalledTimes(2);

      setVisibility("visible");
      await vi.advanceTimersByTimeAsync(0);
      expect(loadPulls).toHaveBeenCalledTimes(3);

      await vi.advanceTimersByTimeAsync(15_000);
      expect(loadPulls).toHaveBeenCalledTimes(4);
      view.unmount();
    } finally {
      visibilityState = "visible";
    }
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
    ["mrs" as const, loadPulls],
    ["issues" as const, loadIssues],
  ])("loads mobile %s results in bounded chunks and autoloads the next chunk", async (listType, loadList) => {
    const observed = new Map<Element, IntersectionObserverCallback>();
    class IntersectionObserverStub {
      constructor(private readonly callback: IntersectionObserverCallback) {}
      observe(target: Element): void {
        observed.set(target, this.callback);
      }
      disconnect(): void {}
      unobserve(): void {}
      takeRecords(): IntersectionObserverEntry[] {
        return [];
      }
      readonly root = null;
      readonly rootMargin = "0px";
      readonly thresholds = [0];
    }
    vi.stubGlobal("IntersectionObserver", IntersectionObserverStub);
    if (listType === "mrs") pullListCapped.state!.set("capped", true);
    else issueListCapped.state!.set("capped", true);

    const { container } = render(FocusListView, {
      props: { listType, repo: "acme/one", chunked: true },
    });

    expect(loadList).toHaveBeenCalledWith({ repo: "acme/one", limit: 30 });
    const sentinel = container.querySelector(".focus-list-loading-sentinel");
    expect(sentinel).toBeTruthy();
    const notify = observed.get(sentinel!);
    expect(notify).toBeTruthy();

    notify!([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);

    expect(loadList).toHaveBeenLastCalledWith({ repo: "acme/one", limit: 60 });
  });

  it.each([
    ["mrs" as const, pullListCapped, loadMorePulls, loadMoreIssues],
    ["issues" as const, issueListCapped, loadMoreIssues, loadMorePulls],
  ])(
    "loads more non-chunked %s and removes the sentinel at the end",
    async (listType, capped, loadMore, otherLoadMore) => {
      let notify: IntersectionObserverCallback | undefined;
      class IntersectionObserverStub {
        constructor(callback: IntersectionObserverCallback) {
          notify = callback;
        }
        observe(): void {}
        disconnect(): void {}
        unobserve(): void {}
      }
      vi.stubGlobal("IntersectionObserver", IntersectionObserverStub);
      capped.state!.set("capped", true);
      const { container } = render(FocusListView, { props: { listType, chunked: false } });

      expect(container.querySelector(".focus-list-loading-sentinel")).toBeTruthy();
      expect(notify).toBeTruthy();
      notify!([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);
      expect(loadMore).toHaveBeenCalledTimes(1);
      expect(otherLoadMore).not.toHaveBeenCalled();

      capped.state!.set("capped", false);
      await vi.advanceTimersByTimeAsync(0);
      expect(container.querySelector(".focus-list-loading-sentinel")).toBeNull();
    },
  );

  it.each([
    ["mrs" as const, pullListCapped, "PRs"],
    ["issues" as const, issueListCapped, "issues"],
  ])("marks the %s count with + while the list is capped", async (listType, capped, label) => {
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        observe(): void {}
        disconnect(): void {}
        unobserve(): void {}
      },
    );
    capped.state!.set("capped", true);
    const { container } = render(FocusListView, { props: { listType, repo: "acme/one" } });
    const badge = container.querySelector(".count-badge");

    expect(badge?.textContent).toBe(`0+ ${label}`);

    capped.state!.set("capped", false);
    await vi.advanceTimersByTimeAsync(0);
    expect(badge?.textContent).toBe(`0 ${label}`);
  });

  it("withholds No issues found while a capped page has no visible issues", async () => {
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        observe(): void {}
        disconnect(): void {}
        unobserve(): void {}
      },
    );
    issueListCapped.state!.set("capped", true);
    const { container } = render(FocusListView, { props: { listType: "issues", repo: "acme/one" } });

    expect(screen.queryByText("No issues found.")).toBeNull();
    expect(container.querySelector(".focus-list-loading-sentinel")).toBeTruthy();

    issueListCapped.state!.set("capped", false);
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByText("No issues found.")).toBeTruthy();
  });

  it.each(["mrs", "issues"] as const)("discloses %s filters from an icon-only control", async (listType) => {
    const { container } = render(FocusListView, { props: { listType, repo: "acme/one", phonePresentation: true } });

    const filters = screen.getByRole("button", { name: "Filters" });
    expect(filters.textContent?.trim()).toBe("");
    expect(filters.getAttribute("aria-expanded")).toBe("false");
    expect(container.querySelector(".filter-bar")?.classList.contains("filter-bar--expanded")).toBe(false);

    await fireEvent.click(filters);

    expect(filters.getAttribute("aria-expanded")).toBe("true");
    expect(container.querySelector(".filter-bar")?.classList.contains("filter-bar--expanded")).toBe(true);
  });

  it.each([
    ["mrs" as const, loadPulls],
    ["issues" as const, loadIssues],
  ])("applies saved repository presets to the mobile %s query", async (listType, loadList) => {
    render(FocusListView, { props: { listType, showRepoSelector: true, phonePresentation: true } });

    expect(screen.queryByRole("button", { name: "Select repository: Global" })).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Filters" }));
    await fireEvent.click(screen.getByRole("button", { name: "Select repository: Global" }));
    await fireEvent.mouseDown(screen.getByRole("option", { name: "Backend" }));

    expect(getGlobalRepo()).toBe("github|github.com/acme/api");
    expect(loadList).toHaveBeenLastCalledWith({ repo: "github|github.com/acme/api" });
    expect(screen.queryByRole("button", { name: "Save preset" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete preset Backend" })).toBeNull();
  });

  it.each([
    ["mrs" as const, setPullsInvolvesMe, loadPulls],
    ["issues" as const, setIssuesInvolvesMe, loadIssues],
  ])("uses the shared Involves me control for %s", async (listType, setInvolvesMe, loadList) => {
    render(FocusListView, { props: { listType, repo: "acme/one", phonePresentation: true } });
    await fireEvent.click(screen.getByRole("button", { name: "Filters" }));
    loadList.mockClear();

    const control = screen.getByRole("button", { name: "Involves me" });
    expect(control.getAttribute("aria-pressed")).toBe("false");
    await fireEvent.click(control);

    expect(setInvolvesMe).toHaveBeenLastCalledWith("require");
    expect(loadList).toHaveBeenCalledTimes(1);
    expect(control.getAttribute("aria-pressed")).toBe("true");

    // A second click asks for the opposite, and a third turns the filter off.
    await fireEvent.click(control);
    expect(setInvolvesMe).toHaveBeenLastCalledWith("exclude");
    expect(loadList).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Involves me, excluded" })).toBe(control);
    expect(control.getAttribute("aria-pressed")).toBe("true");

    await fireEvent.click(control);
    expect(setInvolvesMe).toHaveBeenLastCalledWith(undefined);
    expect(screen.getByRole("button", { name: "Involves me" }).getAttribute("aria-pressed")).toBe("false");
  });

  it.each([
    ["mrs" as const, setPullsUnassigned, loadPulls],
    ["issues" as const, setIssuesUnassigned, loadIssues],
  ])("uses the shared Unassigned control for %s", async (listType, setUnassigned, loadList) => {
    render(FocusListView, { props: { listType, repo: "acme/one", phonePresentation: true } });
    await fireEvent.click(screen.getByRole("button", { name: "Filters" }));
    loadList.mockClear();

    const control = screen.getByRole("button", { name: "Unassigned" });
    expect(control.getAttribute("aria-pressed")).toBe("false");
    await fireEvent.click(control);

    expect(setUnassigned).toHaveBeenLastCalledWith("require");
    expect(loadList).toHaveBeenCalledTimes(1);
    expect(control.getAttribute("aria-pressed")).toBe("true");

    // A second click asks for the opposite, and a third turns the filter off.
    await fireEvent.click(control);
    expect(setUnassigned).toHaveBeenLastCalledWith("exclude");
    expect(loadList).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Unassigned, excluded" })).toBe(control);
    expect(control.getAttribute("aria-pressed")).toBe("true");

    await fireEvent.click(control);
    expect(setUnassigned).toHaveBeenLastCalledWith(undefined);
    expect(screen.getByRole("button", { name: "Unassigned" }).getAttribute("aria-pressed")).toBe("false");
  });

  it("filters issues by pull request references", async () => {
    render(FocusListView, { props: { listType: "issues", repo: "acme/one", phonePresentation: true } });
    await fireEvent.click(screen.getByRole("button", { name: "Filters" }));
    loadIssues.mockClear();

    const control = screen.getByRole("button", { name: "Referenced by PR" });
    expect(control.getAttribute("aria-pressed")).toBe("false");
    await fireEvent.click(control);

    expect(setIssuesReferencedByPR).toHaveBeenLastCalledWith("require");
    expect(loadIssues).toHaveBeenCalledTimes(1);
    expect(control.getAttribute("aria-pressed")).toBe("true");

    await fireEvent.click(control);
    expect(setIssuesReferencedByPR).toHaveBeenLastCalledWith("exclude");
    expect(loadIssues).toHaveBeenCalledTimes(2);
  });
});
