import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
import { createClaimTestController } from "./viewWorkspaceTestDoubles.svelte.js";

vi.mock("../components/ActivityFeed.svelte", async () => ({
  default: (await import("./ActivityFeedViewTestActivityFeed.svelte")).default,
}));
vi.mock("./PRListView.svelte", async () => ({
  default: (await import("./ActivityFeedViewTestPRListView.svelte")).default,
}));
vi.mock("./IssueListView.svelte", async () => ({
  default: (await import("./ActivityFeedViewTestIssueListView.svelte")).default,
}));

import ActivityFeedView from "./ActivityFeedView.svelte";

describe("ActivityFeedView inline workspace", () => {
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

  it("threads the controller to the embedded PR view with renderWorkspaceDock=false and renders a single dock", () => {
    const { controller } = createClaimTestController("activity");
    const drawerItem = {
      itemType: "pr" as const,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
      owner: "acme",
      name: "widgets",
      number: 12,
      detailTab: "conversation" as const,
    };

    render(ActivityFeedView, {
      props: {
        drawerItem,
        inlineWorkspace: controller,
      },
    });

    const embeddedPR = screen.getByTestId("embedded-pr-list-view");
    expect(embeddedPR.getAttribute("data-render-workspace-dock")).toBe("false");
    expect(embeddedPR.getAttribute("data-has-inline-workspace")).toBe("true");
    expect(screen.queryByTestId("embedded-issue-list-view")).toBeNull();

    // ActivityFeedView owns the single dock for its embedded views; the
    // embed itself never renders one (renderWorkspaceDock=false above).
    expect(document.querySelectorAll(".workspace-dock-panel")).toHaveLength(1);
  });

  it("threads the controller to the embedded issue view without wrapping it in a dock", () => {
    const { controller } = createClaimTestController("activity");
    const drawerItem = {
      itemType: "issue" as const,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
      owner: "acme",
      name: "widgets",
      number: 9,
      detailTab: "conversation" as const,
    };

    render(ActivityFeedView, {
      props: {
        drawerItem,
        inlineWorkspace: controller,
      },
    });

    const embeddedIssue = screen.getByTestId("embedded-issue-list-view");
    expect(embeddedIssue.getAttribute("data-has-inline-workspace")).toBe("true");
    expect(screen.queryByTestId("embedded-pr-list-view")).toBeNull();

    // The embedded issue view now hosts the workspace as one of its own panes.
    // Wrapping it in the outer dock as well would put two portal slots on the
    // page, and whichever registered last would silently win the terminal.
    expect(document.querySelectorAll(".workspace-dock-panel")).toHaveLength(0);
  });

  it("renders no dock when there is no inline workspace controller", () => {
    const drawerItem = {
      itemType: "pr" as const,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widgets",
      owner: "acme",
      name: "widgets",
      number: 12,
      detailTab: "conversation" as const,
    };

    render(ActivityFeedView, { props: { drawerItem } });

    const embeddedPR = screen.getByTestId("embedded-pr-list-view");
    expect(embeddedPR.getAttribute("data-has-inline-workspace")).toBe("false");
    expect(document.querySelectorAll(".workspace-dock-panel")).toHaveLength(0);
  });
});
