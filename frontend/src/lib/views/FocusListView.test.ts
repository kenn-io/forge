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
import { getGlobalRepo, setGlobalRepo } from "../stores/filter.svelte.js";

const pullSearch = vi.hoisted(() => vi.fn());
const issueSearch = vi.hoisted(() => vi.fn());
const loadPulls = vi.hoisted(() => vi.fn());
const loadIssues = vi.hoisted(() => vi.fn());
const setPullsInvolvesMe = vi.hoisted(() => vi.fn());
const setIssuesInvolvesMe = vi.hoisted(() => vi.fn());
const setIssuesReferencedByPR = vi.hoisted(() => vi.fn());
const unsubscribeSync = vi.hoisted(() => vi.fn());
const subscribeSyncComplete = vi.hoisted(() => vi.fn(() => unsubscribeSync));
const pullListCapped = vi.hoisted(() => ({ value: false }));
const issueListCapped = vi.hoisted(() => ({ value: false }));
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
      isIssueListCapped: () => issueListCapped.value,
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
      isListCapped: () => pullListCapped.value,
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
      getConfiguredRepos: () => [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
          platform_repo_id: "R_api",
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
              platform_repo_id: "R_api",
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
    pullListCapped.value = false;
    issueListCapped.value = false;
    setGlobalRepo(undefined);
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
    ["mrs" as const, loadPulls, pullListCapped],
    ["issues" as const, loadIssues, issueListCapped],
  ])("loads mobile %s results in bounded chunks and autoloads the next chunk", async (listType, loadList, capped) => {
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
    capped.value = true;

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

  it.each(["mrs", "issues"] as const)("discloses %s filters from an icon-only control", async (listType) => {
    const { container } = render(FocusListView, { props: { listType, repo: "acme/one" } });

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
    render(FocusListView, { props: { listType, showRepoSelector: true } });

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
