import { cleanup, render, screen } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { SIDEBAR_KEY, STORES_KEY } from "../context.js";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
import type { IssueRouteRef } from "../routes.js";
import type { InlineWorkspaceController } from "../workspace-inline.js";
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
  renderWorkspaceDock?: boolean;
  detail?: unknown;
}

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
        [STORES_KEY, { issues: issuesStore }],
      ]),
    }),
  };
}

describe("IssueListView inline workspace", () => {
  beforeEach(() => {
    localStorage.clear();
    resetModalStack();
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
    localStorage.clear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
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

  it("renders the dock only when renderWorkspaceDock and claimed", () => {
    const detail = issueDetailFixture({ id: "ws-1", status: "ready" });

    // renderWorkspaceDock={false}: no dock even though the detail claims
    // the workspace normally.
    {
      const { controller } = createClaimTestController();
      renderIssueListView({ inlineWorkspace: controller, renderWorkspaceDock: false, detail });
      expect(controller.claim).toHaveBeenCalled();
      expect(document.querySelector(".workspace-dock-panel")).toBeNull();
      expect(screen.getByTestId("issue-detail")).toBeTruthy();
      cleanup();
    }

    // Default renderWorkspaceDock (true): the dock renders once claimed.
    {
      const { controller } = createClaimTestController();
      renderIssueListView({ inlineWorkspace: controller, detail });
      expect(controller.claim).toHaveBeenCalled();
      expect(document.querySelector(".workspace-dock-panel")).toBeTruthy();
      expect(screen.getByRole("region", { name: "Workspace terminal" })).toBeTruthy();
      cleanup();
    }
  });

  it("refetches the detail when the claimed identity is invalidated by deletion", async () => {
    const { controller, notifyInvalidated } = createClaimTestController();
    const detail = issueDetailFixture({ id: "ws-1", status: "ready" });
    const { issuesStore, detailBox } = renderIssueListView({ inlineWorkspace: controller, detail });

    expect(controller.claim).toHaveBeenCalled();
    expect(screen.getByRole("region", { name: "Workspace terminal" })).toBeTruthy();

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
    expect(screen.queryByRole("region", { name: "Workspace terminal" })).toBeNull();
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
