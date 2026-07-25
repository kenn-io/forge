import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { STORES_KEY } from "../context.js";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
import { resetPaneLayoutStoresForTest } from "../stores/paneLayout.svelte.js";
import { createClaimTestController, createReactiveValue } from "./viewWorkspaceTestDoubles.svelte.js";
import type { InlineWorkspaceController } from "../workspace-inline.js";

vi.mock("../components/ActivityFeed.svelte", async () => ({
  default: (await import("./ActivityFeedViewTestActivityFeed.svelte")).default,
}));
vi.mock("../components/detail/PullDetailPane.svelte", async () => ({
  default: (await import("./ActivityFeedViewTestPullDetailPane.svelte")).default,
}));
vi.mock("../components/detail/IssueDetail.svelte", async () => ({
  default: (await import("./IssueListViewTestIssueDetail.svelte")).default,
}));
vi.mock("../components/CommitDiffPanel.svelte", async () => ({
  default: (await import("./ActivityFeedViewTestCommitDiffPanel.svelte")).default,
}));

import ActivityFeedView from "./ActivityFeedView.svelte";

const repo = {
  provider: "github",
  platformHost: "github.com",
  repoPath: "acme/widgets",
  owner: "acme",
  name: "widgets",
};

function prDrawer(number = 12) {
  return { ...repo, itemType: "pr" as const, number, detailTab: "conversation" as const };
}

function issueDrawer(number = 9) {
  return { ...repo, itemType: "issue" as const, number, detailTab: "conversation" as const };
}

function pullDetailFixture(number: number, workspace?: { id: string; status: string }) {
  return {
    repo_owner: repo.owner,
    repo_name: repo.name,
    merge_request: { Number: number },
    repo: { provider: repo.provider, platform_host: repo.platformHost, repo_path: repo.repoPath },
    workspace,
  };
}

function issueDetailFixture(number: number, workspace?: { id: string; status: string }) {
  return {
    repo_owner: repo.owner,
    repo_name: repo.name,
    issue: { Number: number },
    repo: { provider: repo.provider, platform_host: repo.platformHost, repo_path: repo.repoPath },
    workspace,
  };
}

interface RenderOptions {
  drawerItem?: unknown;
  inlineWorkspace?: InlineWorkspaceController | null;
  pullDetail?: unknown;
  issueDetail?: unknown;
}

function renderActivity(options: RenderOptions = {}) {
  const pullBox = createReactiveValue(options.pullDetail ?? null);
  const issueBox = createReactiveValue(options.issueDetail ?? null);
  const stores = {
    detail: { getDetail: pullBox.get, loadDetail: vi.fn(async () => undefined) },
    issues: { getIssueDetail: issueBox.get, loadIssueDetail: vi.fn(async () => undefined) },
  };

  return {
    stores,
    pullBox,
    issueBox,
    ...render(ActivityFeedView, {
      props: {
        drawerItem: options.drawerItem ?? null,
        ...(options.inlineWorkspace !== undefined ? { inlineWorkspace: options.inlineWorkspace } : {}),
      },
      context: new Map<symbol, unknown>([[STORES_KEY, stores]]),
    }),
  };
}

describe("ActivityFeedView detail panes", () => {
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

  it("offers the diff pane for a PR selection", () => {
    renderActivity({ drawerItem: prDrawer(), pullDetail: pullDetailFixture(12) });

    expect(screen.getByRole("tab", { name: "Conversation" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Files changed" })).toBeTruthy();
    // One body per tab in the leaf; only the active one is on screen.
    const bodies = screen.getAllByTestId("pull-detail-pane");
    expect(bodies.map((el) => el.getAttribute("data-tab-key")).sort()).toEqual(["conversation", "files"]);
    expect(bodies.find((el) => el.getAttribute("data-visible") === "true")?.getAttribute("data-tab-key")).toBe(
      "conversation",
    );
  });

  it("offers no diff pane for an issue selection", () => {
    // An issue has no diff, so the pane is unavailable and prunes out of the
    // tree rather than rendering an empty body.
    renderActivity({ drawerItem: issueDrawer(), issueDetail: issueDetailFixture(9) });

    expect(screen.getByRole("tab", { name: "Conversation" })).toBeTruthy();
    expect(screen.queryByRole("tab", { name: "Files changed" })).toBeNull();
    expect(screen.getByTestId("issue-detail")).toBeTruthy();
    expect(screen.queryByTestId("pull-detail-pane")).toBeNull();
  });

  it("offers the commit pane for a branch commit selection", async () => {
    const { component } = renderActivity({ drawerItem: null });
    // The feed double exposes the branch-commit callback the real feed fires.
    screen.getByTestId("select-branch-commit").click();
    await Promise.resolve();
    expect(component).toBeTruthy();

    expect(screen.getByRole("tab", { name: "Commit" })).toBeTruthy();
    expect(screen.queryByRole("tab", { name: "Conversation" })).toBeNull();
    expect(screen.getByTestId("commit-diff-panel")).toBeTruthy();
  });

  it("offers a workspace pane only once the workspace is claimed", () => {
    {
      const { controller } = createClaimTestController("activity");
      renderActivity({
        drawerItem: prDrawer(),
        inlineWorkspace: controller,
        pullDetail: pullDetailFixture(12),
      });
      expect(controller.claim).not.toHaveBeenCalled();
      expect(screen.queryByRole("tab", { name: "Workspace" })).toBeNull();
      expect(document.querySelector(".detail-pane-workspace-slot")).toBeNull();
      cleanup();
    }

    {
      const { controller } = createClaimTestController("activity");
      renderActivity({
        drawerItem: prDrawer(),
        inlineWorkspace: controller,
        pullDetail: pullDetailFixture(12, { id: "ws-1", status: "ready" }),
      });
      expect(controller.claim).toHaveBeenCalled();
      expect(screen.getByRole("tab", { name: "Workspace" })).toBeTruthy();
      expect(document.querySelector(".detail-pane-workspace-slot")).toBeTruthy();
      cleanup();
    }
  });

  it("claims through the issue store when the selection is an issue", () => {
    const { controller } = createClaimTestController("activity");
    renderActivity({
      drawerItem: issueDrawer(),
      inlineWorkspace: controller,
      // A PR detail for the same repo and number must not satisfy an issue
      // selection: a PR and an issue can share both and own unrelated
      // workspaces.
      pullDetail: pullDetailFixture(9, { id: "ws-pr", status: "ready" }),
      issueDetail: issueDetailFixture(9, { id: "ws-issue", status: "ready" }),
    });

    expect(controller.claim).toHaveBeenCalledWith(expect.objectContaining({ number: 9, itemType: "issue" }), {
      id: "ws-issue",
      status: "ready",
    });
  });

  it("keeps the workspace slot mounted across a PR to issue selection change", async () => {
    const { controller } = createClaimTestController("activity");
    const { rerender } = renderActivity({
      drawerItem: prDrawer(12),
      inlineWorkspace: controller,
      pullDetail: pullDetailFixture(12, { id: "ws-1", status: "ready" }),
      issueDetail: issueDetailFixture(9, { id: "ws-1", status: "ready" }),
    });

    const slot = document.querySelector(".detail-pane-workspace-slot");
    expect(slot).toBeTruthy();

    // One pane tree spans both selection kinds, so the slot element survives the
    // switch and the live terminal is never reparented for it.
    await rerender({ drawerItem: issueDrawer(9), inlineWorkspace: controller });

    expect(document.querySelector(".detail-pane-workspace-slot")).toBe(slot);
  });
});
