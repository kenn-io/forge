import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { NAVIGATE_KEY, SIDEBAR_KEY, STORES_KEY } from "../context.js";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
import { resetPaneLayoutStoresForTest } from "../stores/paneLayout.svelte.js";
import type { PullRequestRouteRef } from "../routes.js";
import type { InlineWorkspaceController } from "../workspace-inline.js";
import { createClaimTestController, createReactiveValue } from "./viewWorkspaceTestDoubles.svelte.js";

const observedWidth = vi.hoisted(() => ({ value: 0 }));

function mockPointerCapture(element: HTMLElement): void {
  Object.defineProperties(element, {
    setPointerCapture: { configurable: true, value: vi.fn() },
    hasPointerCapture: { configurable: true, value: vi.fn(() => true) },
    releasePointerCapture: { configurable: true, value: vi.fn() },
  });
}

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

/** DetailPaneLayout flattens the tree below this measured host width. */
const flattenBelowPx = 720;

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
      [
        {
          contentRect: { width: observedWidth.value },
        } as ResizeObserverEntry,
      ],
      this as unknown as ResizeObserver,
    );
  }

  unobserve(): void {}
  disconnect(): void {}
}

function mockElementRect(): DOMRect {
  return {
    width: observedWidth.value,
    height: 1000,
    x: 0,
    y: 0,
    top: 0,
    right: observedWidth.value,
    bottom: 1000,
    left: 0,
    toJSON: () => ({}),
  };
}

interface RenderPRListViewOptions {
  detailTab?: "conversation" | "files";
  selectedPR?: PullRequestRouteRef | null;
  inlineWorkspace?: InlineWorkspaceController | null;
  detail?: unknown;
  navigate?: (path: string | { path: string }, options?: { replace?: boolean }) => void;
}

function renderPRListView(options: RenderPRListViewOptions = {}) {
  const detailBox = createReactiveValue(options.detail ?? null);
  const detailStore = {
    getDetail: detailBox.get,
    loadDetail: vi.fn(async () => undefined),
  };

  return {
    detailStore,
    detailBox,
    ...render(PRListView, {
      props: {
        selectedPR: options.selectedPR === undefined ? selectedPR : options.selectedPR,
        detailTab: options.detailTab ?? "conversation",
        hideSidebar: true,
        ...(options.inlineWorkspace !== undefined ? { inlineWorkspace: options.inlineWorkspace } : {}),
      },
      context: new Map<symbol, unknown>([
        [
          SIDEBAR_KEY,
          {
            isSidebarToggleEnabled: () => false,
            toggleSidebar: vi.fn(),
          },
        ],
        [NAVIGATE_KEY, options.navigate ?? vi.fn()],
        [STORES_KEY, { detail: detailStore }],
      ]),
    }),
  };
}

describe("PRListView detail panes", () => {
  beforeEach(() => {
    localStorage.clear();
    resetModalStack();
    resetPaneLayoutStoresForTest();
    observedWidth.value = 1600;
    vi.stubGlobal("ResizeObserver", ResizeObserverMock);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(mockElementRect);
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
    resetPaneLayoutStoresForTest();
    localStorage.clear();
    // observedWidth is a module-level fixture shared across describe blocks and
    // test order is shuffled, so a leftover width would leak pane flattening
    // into unrelated tests.
    observedWidth.value = 0;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("shows conversation and files as panes without a bespoke split toggle", async () => {
    renderPRListView();
    await tick();

    expect(screen.queryByRole("button", { name: "Split view" })).toBeNull();
    expect(screen.getByRole("tab", { name: "Conversation" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Files changed" })).toBeTruthy();
    expect(screen.getByTestId("pull-detail").textContent).toContain("Conversation acme/widgets#12");
  });

  it("pushes history on a pane tab click while the panes share a leaf", async () => {
    const navigate = vi.fn();
    renderPRListView({ navigate });
    await tick();

    await fireEvent.click(screen.getByRole("tab", { name: "Files changed" }));

    expect(navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12/files", { replace: false });
  });

  it("replaces history on a tab click once the panes are split apart", async () => {
    // History semantics follow the arrangement, not which control was touched: a
    // pane split into its own leaf still renders a clickable header, and clicking
    // it must behave the same as focusing its body.
    const navigate = vi.fn();
    renderPRListView({ navigate });
    await tick();

    await fireEvent.click(screen.getByTestId("pane-split-right"));
    await tick();
    navigate.mockClear();

    await fireEvent.click(screen.getByRole("tab", { name: "Files changed" }));

    expect(navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12/files", { replace: true });
  });

  it("leaves the route alone when focus moves within one pane group", async () => {
    // Conversation and files start in the same leaf, so only one is on screen and
    // switching between them is a tab click. That path pushes history; a focus
    // event must not also write the route.
    const navigate = vi.fn();
    renderPRListView({ navigate });
    await tick();

    const panes = document.querySelectorAll<HTMLElement>(".tabbed-panel-tab-panel");
    await fireEvent.focusIn(panes[1]!);

    expect(navigate).not.toHaveBeenCalled();
  });

  it("replaces rather than pushes when focus moves between panes split apart", async () => {
    const navigate = vi.fn();
    renderPRListView({ navigate });
    await tick();

    // Split the panes apart: both are visible now, so there is no tab to click
    // and the route has to follow focus instead.
    await fireEvent.click(screen.getByTestId("pane-split-right"));
    await tick();

    const filesPane = screen.getByTestId("diff-files").closest(".tabbed-panel-tab-panel");
    await fireEvent.focusIn(filesPane!);

    // Replace, not push: walking between two panes on screen at once must not
    // fill the Back stack.
    expect(navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12/files", { replace: true });
  });

  it("keeps the diff pane scroll offset across a pane switch", async () => {
    renderPRListView({ detailTab: "files" });
    await tick();

    const diff = screen.getByTestId("diff-files");
    expect(diff.getAttribute("data-initial-scroll-top")).toBe("0");
    Object.defineProperty(diff, "scrollTop", { configurable: true, value: 420 });
    await fireEvent.scroll(diff);

    // Remounting the pane (as a PR switch or a layout change does) must restore
    // the reader's place rather than jump back to the top.
    cleanup();
    renderPRListView({ detailTab: "files" });
    await tick();

    expect(screen.getByTestId("diff-files").getAttribute("data-initial-scroll-top")).toBe("420");
  });

  it("flattens to a single tab strip on a narrow host", async () => {
    observedWidth.value = flattenBelowPx - 1;

    renderPRListView();
    await tick();

    // One leaf, so no divider and no per-leaf split controls: rearranging panes
    // is not offered where there is no room for two of them.
    expect(screen.queryByRole("separator", { name: "Resize detail panes" })).toBeNull();
    expect(screen.queryByTestId("pane-split-right")).toBeNull();
    expect(screen.getAllByRole("tablist")).toHaveLength(1);
  });
});

function pullDetailFixture(workspace: { id: string; status: string } | undefined) {
  return {
    repo_owner: selectedPR.owner,
    repo_name: selectedPR.name,
    merge_request: { Number: selectedPR.number },
    repo: {
      provider: selectedPR.provider,
      platform_host: selectedPR.platformHost,
      repo_path: selectedPR.repoPath,
    },
    workspace,
  };
}

const selectedPRIdentity = {
  provider: selectedPR.provider,
  platformHost: selectedPR.platformHost,
  owner: selectedPR.owner,
  name: selectedPR.name,
  repoPath: selectedPR.repoPath,
  number: selectedPR.number,
  itemType: "pull",
};

describe("PRListView inline workspace", () => {
  beforeEach(() => {
    localStorage.clear();
    resetModalStack();
    resetPaneLayoutStoresForTest();
    vi.stubGlobal(
      "MutationObserver",
      class {
        observe(): void {}
        disconnect(): void {}
      },
    );
    vi.stubGlobal("requestAnimationFrame", () => 1);
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
    resetPaneLayoutStoresForTest();
    localStorage.clear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("claims when the loaded detail matches the selection and carries a workspace", () => {
    const { controller } = createClaimTestController();
    renderPRListView({
      inlineWorkspace: controller,
      detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(controller.claim).toHaveBeenCalledWith(selectedPRIdentity, { id: "ws-1", status: "ready" });
    expect(controller.release).not.toHaveBeenCalled();
  });

  it("claims when the selection omits the host and the detail carries the provider default", () => {
    // Activity URLs may omit platform_host while the loaded detail always
    // carries the concrete default host; the match guard must treat them
    // as one item instead of releasing the claim.
    const { controller } = createClaimTestController();
    renderPRListView({
      inlineWorkspace: controller,
      selectedPR: { ...selectedPR, platformHost: undefined },
      detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(controller.claim).toHaveBeenCalledWith(
      { ...selectedPRIdentity, platformHost: undefined },
      { id: "ws-1", status: "ready" },
    );
    expect(controller.release).not.toHaveBeenCalled();
  });

  it("releases on stale detail, missing workspace, or cleared selection", () => {
    // (a) stale detail: loaded for a different identity than selectedPR.
    {
      const { controller } = createClaimTestController();
      renderPRListView({
        inlineWorkspace: controller,
        detail: { ...pullDetailFixture({ id: "ws-1", status: "ready" }), repo_owner: "someone-else" },
      });
      expect(controller.claim).not.toHaveBeenCalled();
      expect(controller.release).toHaveBeenCalled();
      cleanup();
    }

    // (b) matching detail, no workspace ref and no override.
    {
      const { controller } = createClaimTestController();
      renderPRListView({
        inlineWorkspace: controller,
        detail: pullDetailFixture(undefined),
      });
      expect(controller.claim).not.toHaveBeenCalled();
      expect(controller.release).toHaveBeenCalled();
      cleanup();
    }

    // (c) selection cleared.
    {
      const { controller } = createClaimTestController();
      renderPRListView({
        inlineWorkspace: controller,
        selectedPR: null,
        detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
      });
      expect(controller.claim).not.toHaveBeenCalled();
      expect(controller.release).toHaveBeenCalled();
      cleanup();
    }
  });

  it("releases on unmount", () => {
    const { controller } = createClaimTestController();
    const { unmount } = renderPRListView({
      inlineWorkspace: controller,
      detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(controller.claim).toHaveBeenCalled();
    expect(controller.release).not.toHaveBeenCalled();

    unmount();

    expect(controller.release).toHaveBeenCalled();
  });

  it("offers a workspace pane only once the workspace is claimed", () => {
    // Unclaimed: the pane is unavailable, so it prunes out of the tree and no
    // portal slot exists to steal the single live terminal.
    {
      const { controller } = createClaimTestController();
      renderPRListView({ inlineWorkspace: controller, detail: pullDetailFixture(undefined) });
      expect(controller.claim).not.toHaveBeenCalled();
      expect(screen.getByTestId("pull-detail")).toBeTruthy();
      expect(screen.queryByRole("tab", { name: "Workspace" })).toBeNull();
      expect(document.querySelector(".detail-pane-workspace-slot")).toBeNull();
      cleanup();
    }

    {
      const { controller } = createClaimTestController();
      renderPRListView({
        inlineWorkspace: controller,
        detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
      });
      expect(controller.claim).toHaveBeenCalled();
      expect(screen.getByRole("tab", { name: "Workspace" })).toBeTruthy();
      expect(document.querySelector(".detail-pane-workspace-slot")).toBeTruthy();
      expect(controller.slotAttachment).toHaveBeenCalled();
      cleanup();
    }
  });

  it("refetches the detail when the claimed identity is invalidated by deletion", async () => {
    const { controller, notifyInvalidated } = createClaimTestController();
    const detail = pullDetailFixture({ id: "ws-1", status: "ready" });
    const { detailStore, detailBox } = renderPRListView({ inlineWorkspace: controller, detail });

    expect(controller.claim).toHaveBeenCalled();
    expect(document.querySelector(".detail-pane-workspace-slot")).toBeTruthy();

    notifyInvalidated(selectedPRIdentity);

    expect(detailStore.loadDetail).toHaveBeenCalledWith(selectedPR.owner, selectedPR.name, selectedPR.number, {
      sync: false,
      provider: selectedPR.provider,
      platformHost: selectedPR.platformHost,
      repoPath: selectedPR.repoPath,
    });

    // Simulate the refetch landing without the workspace, as a real
    // deletion followed by a refresh would leave it: the claim effect
    // re-evaluates against the fresh envelope and releases the claim.
    detailBox.set(pullDetailFixture(undefined));
    await tick();

    expect(controller.release).toHaveBeenCalled();
    expect(document.querySelector(".detail-pane-workspace-slot")).toBeNull();
  });

  it("threads inlineWorkspace to PullDetail", () => {
    const { controller } = createClaimTestController();
    renderPRListView({ inlineWorkspace: controller });

    // A dropped `{inlineWorkspace}` on PullDetail's render site would silently
    // revert this surface to the pre-inline-workspace behavior without failing
    // any other assertion.
    expect(screen.getByTestId("pull-detail").getAttribute("data-has-inline-workspace")).toBe("true");
  });
});
