// Vitest component conversion of the grouping-toggle e2e case
// "toggle syncs from PRs to issues" (frontend/tests/e2e-full/grouping-toggle.spec.ts).
// The ungrouped toggle is flipped through PullList's real control, then
// IssueList is rendered with fresh store instances reading the same
// localStorage — the component-test equivalent of navigating to /issues.
import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { Issue, PullRequest } from "../../api/types.js";
import { ACTIONS_KEY, HOST_STATE_KEY, NAVIGATE_KEY, SIDEBAR_KEY, STORES_KEY } from "../../context.js";
import { createCollapsedReposStore } from "../../stores/collapsedRepos.svelte.js";
import { createGroupingStore } from "../../stores/grouping.svelte.js";
import { createIssuesStore } from "../../stores/issues.svelte.js";
import { createPullsStore } from "../../stores/pulls.svelte.js";
import issuesFixtureJson from "../../testing/e2e-fixtures/issues.json";
import pullsFixtureJson from "../../testing/e2e-fixtures/pulls.json";
import type { MiddlemanClient } from "../../types.js";
import IssueList from "./IssueList.svelte";
import PullList from "./PullList.svelte";

// Seed data (captured from the real seeded e2e Go server):
// 8 open PRs and 5 open issues across acme/widgets, acme/tools, and a
// GitLab group/project.
const pullsFixture = pullsFixtureJson as unknown as PullRequest[];
const issuesFixture = issuesFixtureJson as unknown as Issue[];

function fakeClient(): MiddlemanClient {
  return {
    GET: async (path: string) => {
      if (path === "/pulls") {
        return { data: structuredClone(pullsFixture) };
      }
      if (path === "/issues") {
        return { data: structuredClone(issuesFixture) };
      }
      return { error: { title: "unexpected request", detail: path } };
    },
  } as unknown as MiddlemanClient;
}

function sharedStores() {
  const grouping = createGroupingStore();
  return {
    grouping,
    collapsedRepos: createCollapsedReposStore(),
    sync: { getSyncState: () => null, onNextSyncComplete: vi.fn() },
    settings: {
      isSettingsLoaded: () => true,
      hasConfiguredRepos: () => true,
    },
  };
}

function sidebarContext() {
  return {
    isEmbedded: () => true,
    isSidebarToggleEnabled: () => false,
    toggleSidebar: vi.fn(),
  };
}

function renderPullList() {
  const stores = sharedStores();
  const pulls = createPullsStore({
    client: fakeClient(),
    getGroupByRepo: () => stores.grouping.getGroupByRepo(),
  });
  const utils = render(PullList, {
    props: { sidebarWidth: 600, showSelectedDiffSidebar: false },
    context: new Map<symbol, unknown>([
      [STORES_KEY, { pulls, ...stores }],
      [NAVIGATE_KEY, vi.fn()],
      [ACTIONS_KEY, {}],
      [HOST_STATE_KEY, {}],
      [SIDEBAR_KEY, sidebarContext()],
    ]),
  });
  return { ...utils, pulls, ...stores };
}

function renderIssueList() {
  const stores = sharedStores();
  const issues = createIssuesStore({
    client: fakeClient(),
    getGroupByRepo: () => stores.grouping.getGroupByRepo(),
  });
  const utils = render(IssueList, {
    props: { sidebarWidth: 600 },
    context: new Map<symbol, unknown>([
      [STORES_KEY, { issues, ...stores }],
      [NAVIGATE_KEY, vi.fn()],
      [SIDEBAR_KEY, sidebarContext()],
    ]),
  });
  return { ...utils, issues, ...stores };
}

describe("grouping toggle (issue list)", () => {
  beforeEach(() => {
    // Mirrors the e2e beforeEach addInitScript: clear persisted grouping so
    // tests start in default grouped mode.
    localStorage.removeItem("middleman:groupingMode");
    localStorage.removeItem("middleman:groupByRepo");
    localStorage.removeItem("middleman:hideOrgName");
    localStorage.removeItem("middleman:collapsedRepos:pulls");
    localStorage.removeItem("middleman:collapsedRepos:issues");
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("toggle syncs from PRs to issues", async () => {
    // Switch to ungrouped via PullList's real "All" group button.
    const pullsView = renderPullList();
    await waitFor(() => {
      expect(pullsView.container.querySelectorAll(".pull-item").length).toBeGreaterThan(0);
    });
    const allBtn = [...pullsView.container.querySelectorAll<HTMLElement>(".group-btn")].find(
      (b) => b.textContent?.trim() === "All",
    );
    expect(allBtn).toBeDefined();
    await fireEvent.click(allBtn!);
    expect(pullsView.container.querySelectorAll(".repo-header")).toHaveLength(0);
    expect(localStorage.getItem("middleman:groupingMode")).toBe("flat");

    // "Navigate" to issues: unmount and render IssueList with fresh store
    // instances reading the same localStorage.
    cleanup();
    const issuesView = renderIssueList();
    await waitFor(() => {
      expect(issuesView.container.querySelectorAll(".issue-item").length).toBeGreaterThan(0);
    });

    // Issues are ungrouped too: no headers, a repo badge on every item.
    expect(issuesView.container.querySelectorAll(".repo-header")).toHaveLength(0);
    const items = issuesView.container.querySelectorAll(".issue-item");
    expect(items).toHaveLength(5);
    expect(issuesView.container.querySelectorAll(".repo-chip")).toHaveLength(items.length);
  });
});
