import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NAVIGATE_KEY, SIDEBAR_KEY, STORES_KEY } from "../context.js";
import type { PullRequestRouteRef } from "../routes.js";

const observedWidth = vi.hoisted(() => ({ value: 0 }));

vi.mock("../components/sidebar/PullList.svelte", async () => ({
  default: (await import("./PRListViewTestPullList.svelte")).default,
}));

vi.mock("../components/detail/PullDetail.svelte", async () => ({
  default: (await import("./PRListViewTestPullDetail.svelte")).default,
}));

vi.mock("../components/diff/DiffFilesLayout.svelte", async () => ({
  default: (await import("./PRListViewTestDiffFilesLayout.svelte")).default,
}));

import PRListView from "./PRListView.svelte";

const minSplitViewWidth = (800 + 24 + 24) * 2;

const selectedPR: PullRequestRouteRef = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 12,
};

class ResizeObserverMock {
  constructor(private callback: ResizeObserverCallback) {}

  observe(): void {
    this.callback(
      [{ contentRect: { width: observedWidth.value } } as ResizeObserverEntry],
      this as unknown as ResizeObserver,
    );
  }

  disconnect(): void {}
}

function renderPRListView(detailTab: "conversation" | "files" = "conversation") {
  const detailStore = {
    getDetail: () => null,
    loadDetail: vi.fn(async () => undefined),
  };

  return {
    detailStore,
    ...render(PRListView, {
      props: {
        selectedPR,
        detailTab,
        hideSidebar: true,
      },
      context: new Map<symbol, unknown>([
        [
          SIDEBAR_KEY,
          {
            isSidebarToggleEnabled: () => false,
            toggleSidebar: vi.fn(),
          },
        ],
        [NAVIGATE_KEY, vi.fn()],
        [STORES_KEY, { detail: detailStore }],
      ]),
    }),
  };
}

describe("PRListView split view", () => {
  beforeEach(() => {
    localStorage.clear();
    observedWidth.value = minSplitViewWidth;
    vi.stubGlobal("ResizeObserver", ResizeObserverMock);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("does not expose split view below twice the regular conversation width", async () => {
    observedWidth.value = minSplitViewWidth - 1;

    renderPRListView();
    await tick();

    expect(screen.queryByRole("button", { name: "Split view" })).toBeNull();
    expect(screen.getByTestId("pull-detail").textContent).toContain("Conversation acme/widgets#12");
    expect(screen.queryByTestId("diff-files")).toBeNull();
  });

  it("keeps wide split view off until the user enables it", async () => {
    renderPRListView();
    await tick();

    const toggle = screen.getByRole("button", { name: "Split view" });
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    expect(screen.getByTestId("pull-detail")).toBeTruthy();
    expect(screen.queryByTestId("diff-files")).toBeNull();

    await fireEvent.click(toggle);

    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByTestId("pull-detail")).toBeTruthy();
    expect(screen.getByTestId("diff-files")).toBeTruthy();
    expect(localStorage.getItem("pr-detail-split-view")).toBe("1");
  });
});
