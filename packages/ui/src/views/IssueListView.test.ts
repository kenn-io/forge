import { cleanup, render, screen } from "@testing-library/svelte";
import { createRawSnippet, tick, type Snippet } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { SIDEBAR_KEY, STORES_KEY } from "../context.js";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
import { getPaneLayoutStore, resetPaneLayoutStoresForTest } from "../stores/paneLayout.svelte.js";
import type { IssueRouteRef } from "../routes.js";
import type { InlineWorkspaceController } from "../workspace-inline.js";
import { sessionPaneKey } from "../stores/session-pane-key.js";
import { createClaimTestController, createReactiveValue } from "./viewWorkspaceTestDoubles.svelte.js";

vi.mock("../components/sidebar/IssueList.svelte", async () => ({
  default: (await import("./IssueListViewTestIssueList.svelte")).default,
}));

vi.mock("../components/detail/IssueDetail.svelte", async () => ({
  default: (await import("./IssueListViewTestIssueDetail.svelte")).default,
}));

import IssueListView from "./IssueListView.svelte";

const selectedIssue: IssueRouteRef = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 7,
};

const selectedIssueIdentity = {
  provider: selectedIssue.provider,
  platformHost: selectedIssue.platformHost,
  owner: selectedIssue.owner,
  name: selectedIssue.name,
  repoPath: selectedIssue.repoPath,
  number: selectedIssue.number,
  itemType: "issue",
};

function issueDetailFixture(workspace: { id: string; status: string } | undefined) {
  return {
    repo_owner: selectedIssue.owner,
    repo_name: selectedIssue.name,
    issue: { Number: selectedIssue.number },
    repo: {
      provider: selectedIssue.provider,
      platform_host: selectedIssue.platformHost,
      repo_path: selectedIssue.repoPath,
    },
    workspace,
  };
}

interface RenderIssueListViewOptions {
  selectedIssue?: IssueRouteRef | null;
  inlineWorkspace?: InlineWorkspaceController | null;
  detail?: unknown;
  workspacePaneControls?: Snippet | undefined;
}

/** Stands in for the frontend's workspace controls button. */
const controlsDouble: Snippet = createRawSnippet(() => ({
  render: () => `<button type="button" data-testid="workspace-pane-controls">Controls</button>`,
}));

function renderIssueListView(options: RenderIssueListViewOptions = {}) {
  const detailBox = createReactiveValue(options.detail ?? null);
  const issuesStore = {
    getIssueDetail: detailBox.get,
    loadIssueDetail: vi.fn(async () => undefined),
  };

  return {
    issuesStore,
    detailBox,
    ...render(IssueListView, {
      props: {
        selectedIssue: options.selectedIssue === undefined ? selectedIssue : options.selectedIssue,
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
        [STORES_KEY, { issues: issuesStore }],
      ]),
    }),
  };
}

describe("IssueListView inline workspace", () => {
  beforeEach(() => {
    localStorage.clear();
    resetModalStack();
    resetPaneLayoutStoresForTest();
    vi.stubGlobal(
      "MutationObserver",
      class {
        observe(): void {}
        disconnect(): void {}
        takeRecords(): MutationRecord[] {
          return [];
        }
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

  it("renders a promoted session's pane beside the issue conversation", () => {
    const layout = getPaneLayoutStore("issues");
    const paneKey = sessionPaneKey("ws-1", undefined, "ws-1:helper");
    layout.promoteTab(paneKey, { kind: "tab", leafID: layout.leafIDForTab("workspace")! });
    const { controller } = createClaimTestController("issues", {
      sessions: [{ paneKey, label: "Helper" }],
    });

    renderIssueListView({
      inlineWorkspace: controller,
      detail: issueDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(document.querySelector(`[data-session-pane="${paneKey}"]`)).not.toBeNull();
    expect(screen.getByRole("tab", { name: "Helper" })).toBeTruthy();
  });

  it("reports a focused pane to the workspace host", () => {
    const layout = getPaneLayoutStore("issues");
    const paneKey = sessionPaneKey("ws-1", undefined, "ws-1:helper");
    layout.promoteTab(paneKey, { kind: "tab", leafID: layout.leafIDForTab("workspace")! });
    const { controller } = createClaimTestController("issues", {
      sessions: [{ paneKey, label: "Helper" }],
    });

    renderIssueListView({
      inlineWorkspace: controller,
      detail: issueDetailFixture({ id: "ws-1", status: "ready" }),
    });

    document.querySelector(`[data-pane-key="${paneKey}"]`)?.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));

    // Focus is the host's only report of which pane the user is working in, and an
    // expand or a Focus Terminal has to act on that one rather than the container.
    expect(controller.notePaneFocused).toHaveBeenCalledWith(paneKey);
  });

  it("offers the workspace controls in the leaf holding a promoted session", () => {
    const layout = getPaneLayoutStore("issues");
    const paneKey = sessionPaneKey("ws-1", undefined, "ws-1:helper");
    // Placed directly rather than through promoteSessionBesideWorkspace: that
    // helper refuses until a container has reported the workspace pane on screen,
    // and this test is about what the view renders once the pane exists.
    layout.promoteTab(paneKey, {
      kind: "split",
      leafID: layout.leafIDForTab("workspace")!,
      direction: "horizontal",
      placement: "after",
    });
    const { controller } = createClaimTestController("issues", {
      sessions: [{ paneKey, label: "Helper" }],
    });

    renderIssueListView({
      inlineWorkspace: controller,
      detail: issueDetailFixture({ id: "ws-1", status: "ready" }),
      workspacePaneControls: controlsDouble,
    });

    // Each surface wires this separately, so each needs its own proof: the
    // workspace leaf and the promoted session's leaf get the button, the leaf of
    // route panes does not.
    expect(screen.getAllByTestId("workspace-pane-controls")).toHaveLength(2);
    expect(
      document
        .querySelector('[data-pane-key="conversation"]')
        ?.closest(".tabbed-panel-leaf")
        ?.querySelector('[data-testid="workspace-pane-controls"]'),
    ).toBeNull();
  });

  it("claims when the loaded detail matches the selection and carries a workspace", () => {
    const { controller } = createClaimTestController();
    renderIssueListView({
      inlineWorkspace: controller,
      detail: issueDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(controller.claim).toHaveBeenCalledWith(selectedIssueIdentity, { id: "ws-1", status: "ready" });
    expect(controller.release).not.toHaveBeenCalled();
  });

  it("claims when the selection omits the host and the detail carries the provider default", () => {
    // Activity URLs may omit platform_host while the loaded detail always
    // carries the concrete default host; the match guard must treat them
    // as one item instead of releasing the claim.
    const { controller } = createClaimTestController();
    renderIssueListView({
      inlineWorkspace: controller,
      selectedIssue: { ...selectedIssue, platformHost: undefined },
      detail: issueDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(controller.claim).toHaveBeenCalledWith(
      { ...selectedIssueIdentity, platformHost: undefined },
      { id: "ws-1", status: "ready" },
    );
    expect(controller.release).not.toHaveBeenCalled();
  });

  it("releases on stale detail, missing workspace, or cleared selection", () => {
    // (a) stale detail: loaded for a different identity than selectedIssue.
    {
      const { controller } = createClaimTestController();
      renderIssueListView({
        inlineWorkspace: controller,
        detail: { ...issueDetailFixture({ id: "ws-1", status: "ready" }), repo_owner: "someone-else" },
      });
      expect(controller.claim).not.toHaveBeenCalled();
      expect(controller.release).toHaveBeenCalled();
      cleanup();
    }

    // (b) matching detail, no workspace ref and no override.
    {
      const { controller } = createClaimTestController();
      renderIssueListView({
        inlineWorkspace: controller,
        detail: issueDetailFixture(undefined),
      });
      expect(controller.claim).not.toHaveBeenCalled();
      expect(controller.release).toHaveBeenCalled();
      cleanup();
    }

    // (c) selection cleared.
    {
      const { controller } = createClaimTestController();
      renderIssueListView({
        inlineWorkspace: controller,
        selectedIssue: null,
        detail: issueDetailFixture({ id: "ws-1", status: "ready" }),
      });
      expect(controller.claim).not.toHaveBeenCalled();
      expect(controller.release).toHaveBeenCalled();
      cleanup();
    }
  });

  it("releases on unmount", () => {
    const { controller } = createClaimTestController();
    const { unmount } = renderIssueListView({
      inlineWorkspace: controller,
      detail: issueDetailFixture({ id: "ws-1", status: "ready" }),
    });

    expect(controller.claim).toHaveBeenCalled();
    expect(controller.release).not.toHaveBeenCalled();

    unmount();

    expect(controller.release).toHaveBeenCalled();
  });

  it("offers a workspace pane only once the workspace is claimed", () => {
    // Unclaimed: the workspace tab is unavailable, so the tree prunes to the
    // conversation pane alone and no portal slot exists to steal the terminal.
    {
      const { controller } = createClaimTestController();
      renderIssueListView({ inlineWorkspace: controller, detail: issueDetailFixture(undefined) });
      expect(controller.claim).not.toHaveBeenCalled();
      expect(screen.getByTestId("issue-detail")).toBeTruthy();
      expect(screen.queryByRole("tab", { name: "Workspace" })).toBeNull();
      expect(document.querySelector(".detail-pane-workspace-slot")).toBeNull();
      cleanup();
    }

    // Claimed: the workspace pane joins the tree with a live portal slot.
    {
      const { controller } = createClaimTestController();
      renderIssueListView({
        inlineWorkspace: controller,
        detail: issueDetailFixture({ id: "ws-1", status: "ready" }),
      });
      expect(controller.claim).toHaveBeenCalled();
      expect(screen.getByRole("tab", { name: "Conversation" })).toBeTruthy();
      expect(screen.getByRole("tab", { name: "Workspace" })).toBeTruthy();
      expect(document.querySelector(".detail-pane-workspace-slot")).toBeTruthy();
      expect(controller.slotAttachment).toHaveBeenCalled();
      cleanup();
    }
  });

  it("hides the workspace pane on demand and offers a way back", async () => {
    const { controller } = createClaimTestController();
    renderIssueListView({
      inlineWorkspace: controller,
      detail: issueDetailFixture({ id: "ws-1", status: "ready" }),
    });

    screen.getByTestId("pane-hide-workspace").click();
    await tick();

    expect(document.querySelector(".detail-pane-workspace-slot")).toBeNull();
    expect(screen.getByTestId("issue-detail")).toBeTruthy();

    // The reopen strip is the only way back, so it must survive the pane's leaf
    // emptying out.
    screen.getByRole("button", { name: "Show Workspace" }).click();
    await tick();

    expect(document.querySelector(".detail-pane-workspace-slot")).toBeTruthy();
  });

  it("refetches the detail when the claimed identity is invalidated by deletion", async () => {
    const { controller, notifyInvalidated } = createClaimTestController();
    const detail = issueDetailFixture({ id: "ws-1", status: "ready" });
    const { issuesStore, detailBox } = renderIssueListView({ inlineWorkspace: controller, detail });

    expect(controller.claim).toHaveBeenCalled();
    expect(document.querySelector(".detail-pane-workspace-slot")).toBeTruthy();

    notifyInvalidated(selectedIssueIdentity);

    expect(issuesStore.loadIssueDetail).toHaveBeenCalledWith(
      selectedIssue.owner,
      selectedIssue.name,
      selectedIssue.number,
      {
        sync: false,
        provider: selectedIssue.provider,
        platformHost: selectedIssue.platformHost,
        repoPath: selectedIssue.repoPath,
      },
    );

    // Simulate the refetch landing without the workspace, as a real
    // deletion followed by a refresh would leave it: the claim effect
    // re-evaluates against the fresh envelope and releases the claim.
    detailBox.set(issueDetailFixture(undefined));
    await tick();

    expect(controller.release).toHaveBeenCalled();
    expect(document.querySelector(".detail-pane-workspace-slot")).toBeNull();
  });

  it("threads inlineWorkspace to IssueDetail", () => {
    const { controller } = createClaimTestController();
    renderIssueListView({ inlineWorkspace: controller });

    // A dropped `{inlineWorkspace}` on IssueDetail's render site would
    // silently revert this surface to the pre-inline-dock behavior without
    // failing any other assertion.
    expect(screen.getByTestId("issue-detail").getAttribute("data-has-inline-workspace")).toBe("true");
  });
});
