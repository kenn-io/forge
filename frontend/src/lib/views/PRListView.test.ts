import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { createRawSnippet, flushSync, tick, type Snippet } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { NAVIGATE_KEY, SIDEBAR_KEY, STORES_KEY } from "../context.js";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
import { getPaneLayoutStore, resetPaneLayoutStoresForTest } from "../stores/paneLayout.svelte.js";
import { sessionPaneKey } from "../stores/session-pane-key.js";
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

import PRListView from "./PRListViewRuntimeHarness.svelte";

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
  workspacePaneControls?: Snippet | undefined;
}

/** Stands in for the frontend's workspace controls button. */
const controlsDouble: Snippet = createRawSnippet(() => ({
  render: () => `<button type="button" data-testid="workspace-pane-controls">Controls</button>`,
}));

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
        ...(options.workspacePaneControls !== undefined
          ? { workspacePaneControls: options.workspacePaneControls }
          : {}),
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

    // Split through the store: the per-leaf split buttons are gone, and dragging a
    // tab to a pane edge is the user-facing route -- neither of which is this
    // test's subject.
    const layout = getPaneLayoutStore("prs");
    layout.splitTab("files", layout.leafIDForTab("files")!, "horizontal", "after");
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
    const layout = getPaneLayoutStore("prs");
    layout.splitTab("files", layout.leafIDForTab("files")!, "horizontal", "after");
    await tick();

    const filesPane = screen.getByTestId("diff-files").closest(".tabbed-panel-tab-panel");
    await fireEvent.focusIn(filesPane!);

    // Replace, not push: walking between two panes on screen at once must not
    // fill the Back stack.
    expect(navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12/files", { replace: true });
  });

  it("enables diff paging only after focus enters the routed files pane", async () => {
    renderPRListView({ detailTab: "conversation" });
    const layout = getPaneLayoutStore("prs");
    layout.splitTab("files", layout.leafIDForTab("files")!, "horizontal", "after");
    await tick();

    expect(screen.getByTestId("diff-files").dataset.keyboardActive).toBe("false");
    expect(screen.getByTestId("diff-files").dataset.pageKeyboardActive).toBe("false");

    cleanup();
    resetPaneLayoutStoresForTest();

    renderPRListView({ detailTab: "files" });
    const filesLayout = getPaneLayoutStore("prs");
    filesLayout.splitTab("files", filesLayout.leafIDForTab("files")!, "horizontal", "after");
    await tick();

    expect(screen.getByTestId("diff-files").dataset.keyboardActive).toBe("false");
    expect(screen.getByTestId("diff-files").dataset.pageKeyboardActive).toBe("false");

    await fireEvent.focusIn(screen.getByTestId("diff-files"));
    expect(screen.getByTestId("diff-files").dataset.keyboardActive).toBe("true");
    expect(screen.getByTestId("diff-files").dataset.pageKeyboardActive).toBe("true");
  });

  it("moves visible keyboard routing only when focus moves", async () => {
    renderPRListView({ detailTab: "conversation" });
    const layout = getPaneLayoutStore("prs");
    layout.splitTab("files", layout.leafIDForTab("files")!, "horizontal", "after");
    await tick();

    const activePaneKey = () =>
      document.querySelector(".tabbed-panel-leaf.input-active [data-pane-key]")?.getAttribute("data-pane-key");
    expect(activePaneKey()).toBeUndefined();
    expect(screen.getByTestId("diff-files").dataset.keyboardActive).toBe("false");

    await fireEvent.pointerDown(screen.getByTestId("diff-files"));
    await fireEvent.wheel(screen.getByTestId("pull-detail"));
    expect(activePaneKey()).toBeUndefined();
    expect(screen.getByTestId("diff-files").dataset.keyboardActive).toBe("false");
    expect(screen.getByTestId("diff-files").dataset.pageKeyboardActive).toBe("false");

    await fireEvent.focusIn(screen.getByTestId("diff-files"));
    expect(activePaneKey()).toBe("files");
    expect(screen.getByTestId("diff-files").dataset.keyboardActive).toBe("true");
    expect(screen.getByTestId("diff-files").dataset.pageKeyboardActive).toBe("true");

    await fireEvent.focusIn(screen.getByTestId("pull-detail"));
    expect(activePaneKey()).toBe("conversation");
    expect(screen.getByTestId("diff-files").dataset.keyboardActive).toBe("false");
    expect(screen.getByTestId("diff-files").dataset.pageKeyboardActive).toBe("false");
  });

  it("pushes history when the other route pane is covered by a zoom", async () => {
    // Split apart, then maximize the files leaf: the stored tree still says two
    // leaves, but only one pane is on screen, so moving to it is a navigation
    // again. Reading the tree alone replaced history here and silently emptied the
    // Back stack for anyone working maximized.
    const layout = getPaneLayoutStore("prs");
    layout.splitTab("files", layout.leafIDForTab("files")!, "horizontal", "after");

    const navigate = vi.fn();
    renderPRListView({ navigate });
    await tick();
    // Maximized from the layout rather than before mounting: the host reconciles a
    // zoom against what it can actually render on the way up, so a zoom set before
    // the first measurement is dropped as stale.
    layout.toggleZoom(layout.leafIDForTab("files")!);
    await tick();

    await fireEvent.click(screen.getByRole("tab", { name: "Files changed" }));

    expect(navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12/files", { replace: false });
  });

  it("pushes history in a flattened layout even when the stored panes are split apart", async () => {
    // Flattening shows one pane in one strip, so a tab click is a navigation
    // again. Reading the stored leaf ids alone said "split apart" and replaced
    // history, silently emptying the Back stack on a narrow window.
    // Arranged through the store rather than the split control, because the
    // control is suppressed once flattened and the ResizeObserver double only
    // reports width at observe() time.
    const layout = getPaneLayoutStore("prs");
    layout.splitTab("files", layout.leafIDForTab("files")!, "horizontal", "after");
    observedWidth.value = 600;

    const navigate = vi.fn();
    renderPRListView({ navigate });
    await tick();

    await fireEvent.click(screen.getByRole("tab", { name: "Files changed" }));

    expect(navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12/files", { replace: false });
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

    // One leaf, so no divider: there is no second pane to resize against.
    expect(screen.queryByRole("separator", { name: "Resize detail panes" })).toBeNull();
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
      // The slot, not a tab: a leaf holding the workspace alone draws no strip,
      // because the workspace pane already draws one of its own inside.
      expect(document.querySelector(".detail-pane-workspace-slot")).toBeTruthy();
      expect(screen.queryByRole("tab", { name: "Workspace" })).toBeNull();
      expect(controller.slotAttachment).toHaveBeenCalled();
      cleanup();
    }
  });

  it("uses the workspace label supplied by the inline controller", () => {
    const { controller } = createClaimTestController("prs", {
      workspacePaneLabel: "codex (proxy)",
    });

    // Sharing a leaf with the conversation, which is where an outer tab still names
    // the workspace: alone in a leaf it draws no outer strip, because the pane
    // already draws one of its own inside.
    const layout = getPaneLayoutStore("prs");
    layout.appendTabToLeaf("workspace", layout.leafIDForTab("conversation")!);

    renderPRListView({
      inlineWorkspace: controller,
      detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(screen.getByRole("tab", { name: "codex (proxy)" })).toBeTruthy();
  });

  it("anchors the workspace dock row at the surface bottom when its pane retires", () => {
    // Every session promoted into a pane of its own retires the container pane,
    // and the dock lives inside it - so without this the row the user opens a
    // terminal from disappears with the pane, leaving no route to one at all.
    const dockRow = createRawSnippet(() => ({
      render: () => `<div data-testid="dock-row">Terminal</div>`,
    }));
    const { controller } = createClaimTestController("prs", { workspacePaneEmpty: true, dockRow });

    renderPRListView({
      inlineWorkspace: controller,
      detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(screen.getByTestId("dock-row")).toBeTruthy();
    // The pane itself is gone: its body would have rendered nothing.
    expect(document.querySelector(".detail-pane-workspace-slot")).toBeNull();
  });

  it("retires a row-only workspace pane behind its surface dock", () => {
    const dockRow = createRawSnippet(() => ({
      render: () => `<div data-testid="dock-row">Terminal</div>`,
    }));
    const { controller } = createClaimTestController("prs", {
      workspacePaneRowOnly: true,
      dockRow,
    });

    renderPRListView({
      inlineWorkspace: controller,
      detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(screen.getByTestId("dock-row")).toBeTruthy();
    expect(document.querySelector(".detail-pane-workspace-slot")).toBeNull();
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

describe("PRListView promoted session panes", () => {
  const AGENT_PANE = sessionPaneKey("ws-1", undefined, "ws-1:helper");

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
    observedWidth.value = 0;
    vi.restoreAllMocks();
  });

  it("pushes history when a promoted session covers the other route pane", async () => {
    // The arrangement promotion makes possible: panes split apart, with a promoted
    // terminal tabbed in beside the conversation and active while the route is on
    // files. Both leaves render, but the conversation does not - a third pane in a
    // leaf is another way for a route pane to be off screen, so moving to it is a
    // navigation rather than a walk between two visible panes.
    const layout = getPaneLayoutStore("prs");
    layout.splitTab("files", layout.leafIDForTab("files")!, "horizontal", "after");
    layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: layout.leafIDForTab("conversation")! });
    const { controller } = createClaimTestController("prs", {
      sessions: [{ paneKey: AGENT_PANE, label: "Helper" }],
    });

    const navigate = vi.fn();
    renderPRListView({
      navigate,
      detailTab: "files",
      inlineWorkspace: controller,
      detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
    });
    await tick();

    await fireEvent.click(screen.getByRole("tab", { name: "Conversation" }));

    expect(navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/12", { replace: false });
  });

  it("renders a promoted session's pane and reports it visible", () => {
    const layout = getPaneLayoutStore("prs");
    // The workspace pane's own leaf, which the route does not name, so the
    // promoted session is the tab on screen there.
    layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: layout.leafIDForTab("workspace")! });
    const { controller } = createClaimTestController("prs", {
      sessions: [{ paneKey: AGENT_PANE, label: "Helper" }],
    });

    renderPRListView({ inlineWorkspace: controller, detail: pullDetailFixture({ id: "ws-1", status: "ready" }) });

    const pane = document.querySelector(`[data-session-pane="${AGENT_PANE}"]`);
    expect(pane).not.toBeNull();
    expect(pane?.getAttribute("data-session-pane-visible")).toBe("true");
    expect(screen.getByRole("tab", { name: "Helper" })).toBeTruthy();
  });

  it("reports a focused pane to the workspace host", () => {
    const layout = getPaneLayoutStore("prs");
    layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: layout.leafIDForTab("workspace")! });
    const { controller } = createClaimTestController("prs", {
      sessions: [{ paneKey: AGENT_PANE, label: "Helper" }],
    });

    renderPRListView({ inlineWorkspace: controller, detail: pullDetailFixture({ id: "ws-1", status: "ready" }) });

    document
      .querySelector(`[data-pane-key="${AGENT_PANE}"]`)
      ?.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));

    // This view's own focus handler only cares about route-bound panes, so the
    // report has to happen before that filter or the host never hears about the
    // pane the user is actually working in.
    expect(controller.notePaneFocused).toHaveBeenCalledWith(AGENT_PANE);
  });

  it("reports a promoted pane hidden while the route pane owns their shared leaf", () => {
    const layout = getPaneLayoutStore("prs");
    layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: layout.leafIDForTab("conversation")! });
    const { controller } = createClaimTestController("prs", {
      sessions: [{ paneKey: AGENT_PANE, label: "Helper" }],
    });

    renderPRListView({ inlineWorkspace: controller, detail: pullDetailFixture({ id: "ws-1", status: "ready" }) });

    // A deep link is authoritative over stored layout, so the conversation takes
    // the leaf back. The pane still renders - unmounting it would park the
    // terminal - but it must report itself hidden, or a live terminal sits off
    // screen resizing itself to a pane nobody can see.
    const pane = document.querySelector(`[data-session-pane="${AGENT_PANE}"]`);
    expect(pane).not.toBeNull();
    expect(pane?.getAttribute("data-session-pane-visible")).toBe("false");
  });

  it("never conjures a pane for a session the user has not promoted", () => {
    const { controller } = createClaimTestController("prs", {
      sessions: [{ paneKey: AGENT_PANE, label: "Helper" }],
    });

    renderPRListView({ inlineWorkspace: controller, detail: pullDetailFixture({ id: "ws-1", status: "ready" }) });

    // Availability is what the surface offers, not what exists: a session pane
    // exists only because it is already in the stored tree.
    expect(document.querySelector(`[data-session-pane="${AGENT_PANE}"]`)).toBeNull();
    expect(screen.queryByRole("tab", { name: "Helper" })).toBeNull();
  });

  it("offers the workspace controls only in leaves holding the workspace or a session", () => {
    const layout = getPaneLayoutStore("prs");
    // The promoted session gets its own leaf, split off the conversation's, so the
    // three leaves are: route panes, workspace, promoted session.
    layout.promoteTab(AGENT_PANE, {
      kind: "split",
      leafID: layout.leafIDForTab("conversation")!,
      direction: "horizontal",
      placement: "after",
    });
    const { controller } = createClaimTestController("prs", {
      sessions: [{ paneKey: AGENT_PANE, label: "Helper" }],
    });

    renderPRListView({
      inlineWorkspace: controller,
      detail: pullDetailFixture({ id: "ws-1", status: "ready" }),
      workspacePaneControls: controlsDouble,
    });

    // One per subject-bearing leaf, and none in the leaf showing only the
    // conversation and files: controls there would act on nothing.
    expect(screen.getAllByTestId("workspace-pane-controls")).toHaveLength(2);
    const conversationLeaf = document.querySelector('[data-pane-key="conversation"]')?.closest(".tabbed-panel-leaf");
    expect(conversationLeaf?.querySelector('[data-testid="workspace-pane-controls"]')).toBeNull();
  });

  it("prunes a promoted pane when the workspace stops offering it, keeping it stored", async () => {
    const layout = getPaneLayoutStore("prs");
    layout.promoteTab(AGENT_PANE, { kind: "tab", leafID: layout.leafIDForTab("conversation")! });
    const { controller, setSessions } = createClaimTestController("prs", {
      sessions: [{ paneKey: AGENT_PANE, label: "Helper" }],
    });

    renderPRListView({ inlineWorkspace: controller, detail: pullDetailFixture({ id: "ws-1", status: "ready" }) });
    expect(document.querySelector(`[data-session-pane="${AGENT_PANE}"]`)).not.toBeNull();

    // Selecting an item whose workspace has other sessions: the pane must stop
    // rendering, but stay stored so it comes back when that workspace returns.
    setSessions([]);
    flushSync();

    expect(document.querySelector(`[data-session-pane="${AGENT_PANE}"]`)).toBeNull();
    expect(layout.hasTab(AGENT_PANE)).toBe(true);
  });
});
