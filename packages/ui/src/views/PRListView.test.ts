import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { NAVIGATE_KEY, SIDEBAR_KEY, STORES_KEY } from "../context.js";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
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

const minSplitViewWidth = 1280;

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
  renderWorkspaceDock?: boolean;
  detail?: unknown;
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
        ...(options.renderWorkspaceDock !== undefined ? { renderWorkspaceDock: options.renderWorkspaceDock } : {}),
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
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(mockElementRect);
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("does not expose split view below the xl detail-pane width", async () => {
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

  it("lets users resize wide split view panes", async () => {
    observedWidth.value = 2200;

    const { container } = renderPRListView();
    await tick();

    await fireEvent.click(screen.getByRole("button", { name: "Split view" }));
    await tick();

    const conversationPane = container.querySelector<HTMLElement>(".detail-split-pane--conversation");
    expect(conversationPane).not.toBeNull();
    expect(conversationPane!.style.flexBasis).toBe("1098px");

    const resizeHandle = screen.getByRole("separator", {
      name: "Resize PR split view",
    });
    mockPointerCapture(resizeHandle);
    await fireEvent.pointerDown(resizeHandle, { button: 0, clientX: 100, pointerId: 1 });
    await fireEvent.pointerMove(resizeHandle, { clientX: 340, pointerId: 1 });
    await tick();

    expect(conversationPane!.style.flexBasis).toBe("1338px");

    await fireEvent.pointerUp(resizeHandle, { clientX: 340, pointerId: 1 });
    await tick();

    expect(conversationPane!.style.flexBasis).toBe("1338px");
    expect(localStorage.getItem("pr-detail-split-ratio")).toBe("0.6093");
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

  it("renders the dock only when renderWorkspaceDock and claimed", () => {
    const detail = pullDetailFixture({ id: "ws-1", status: "ready" });

    // renderWorkspaceDock={false}: no dock even though the detail claims
    // the workspace normally.
    {
      const { controller } = createClaimTestController();
      renderPRListView({ inlineWorkspace: controller, renderWorkspaceDock: false, detail });
      expect(controller.claim).toHaveBeenCalled();
      expect(document.querySelector(".workspace-dock-panel")).toBeNull();
      expect(screen.getByTestId("pull-detail")).toBeTruthy();
      cleanup();
    }

    // Default renderWorkspaceDock (true): the dock renders once claimed.
    {
      const { controller } = createClaimTestController();
      renderPRListView({ inlineWorkspace: controller, detail });
      expect(controller.claim).toHaveBeenCalled();
      expect(document.querySelector(".workspace-dock-panel")).toBeTruthy();
      expect(screen.getByRole("region", { name: "Workspace terminal" })).toBeTruthy();
      cleanup();
    }
  });

  it("refetches the detail when the claimed identity is invalidated by deletion", async () => {
    const { controller, notifyInvalidated } = createClaimTestController();
    const detail = pullDetailFixture({ id: "ws-1", status: "ready" });
    const { detailStore, detailBox } = renderPRListView({ inlineWorkspace: controller, detail });

    expect(controller.claim).toHaveBeenCalled();
    expect(screen.getByRole("region", { name: "Workspace terminal" })).toBeTruthy();

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
    expect(screen.queryByRole("region", { name: "Workspace terminal" })).toBeNull();
  });

  it("threads inlineWorkspace to both PullDetail render sites (default and split view)", async () => {
    const { controller } = createClaimTestController();
    vi.stubGlobal("ResizeObserver", ResizeObserverMock);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(mockElementRect);
    observedWidth.value = minSplitViewWidth;

    try {
      renderPRListView({ inlineWorkspace: controller });
      await tick();

      // A dropped `{inlineWorkspace}` on either PullDetail call site would
      // silently revert that surface to the pre-inline-dock behavior without
      // failing any other assertion, so both sites are checked directly.
      expect(screen.getByTestId("pull-detail").getAttribute("data-has-inline-workspace")).toBe("true");

      await fireEvent.click(screen.getByRole("button", { name: "Split view" }));
      await tick();

      expect(screen.getByTestId("pull-detail").getAttribute("data-has-inline-workspace")).toBe("true");
    } finally {
      // observedWidth is a module-level fixture shared across describe
      // blocks (and test order is shuffled): leaving it non-zero would leak
      // split-view availability into unrelated tests.
      observedWidth.value = 0;
    }
  });
});
