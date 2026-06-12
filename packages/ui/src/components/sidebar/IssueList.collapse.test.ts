// Vitest component conversion of frontend/tests/e2e-full/collapsible-repos.spec.ts
// (issue-list and cross-surface cases). Uses the REAL grouping/collapsedRepos
// stores plus real pulls/issues stores fed by a fake MiddlemanClient that
// serves the seeded e2e fixture data.
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
// 8 open PRs (widgets 4 + tools 4) and 5 open issues
// (widgets#10/#11/#13, tools#5, GitLab group/project#11).
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

function issueItems(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(".issue-item")];
}

function header(container: HTMLElement, label: string): HTMLElement {
  const headers = [...container.querySelectorAll<HTMLElement>(".repo-header")];
  const match = headers.find((h) => h.querySelector(".repo-header__name")?.textContent === label);
  if (!match) {
    throw new Error(`no .repo-header with name "${label}"`);
  }
  return match;
}

async function renderLoadedIssueList() {
  const utils = renderIssueList();
  await waitFor(() => {
    expect(issueItems(utils.container).length).toBeGreaterThan(0);
  });
  return utils;
}

async function renderLoadedPullList() {
  const utils = renderPullList();
  await waitFor(() => {
    expect(utils.container.querySelectorAll(".pull-item").length).toBeGreaterThan(0);
  });
  return utils;
}

describe("collapsible repo groups (issue list)", () => {
  beforeEach(() => {
    // Mirrors the e2e beforeEach addInitScript: clear collapse state so each
    // test starts expanded. Grouping keys are reset too because jsdom
    // localStorage persists across tests in this file.
    localStorage.removeItem("middleman:collapsedRepos:pulls");
    localStorage.removeItem("middleman:collapsedRepos:issues");
    localStorage.removeItem("middleman:groupingMode");
    localStorage.removeItem("middleman:groupByRepo");
    localStorage.removeItem("middleman:hideOrgName");
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("collapse is independent across pulls and issues surfaces", async () => {
    // Collapse acme/widgets on the pulls surface.
    const pullsView = await renderLoadedPullList();
    await fireEvent.click(header(pullsView.container, "acme/widgets"));
    expect(header(pullsView.container, "acme/widgets").getAttribute("aria-expanded")).toBe("false");

    // "Navigate" to issues: unmount and render the issue list with fresh
    // stores reading the same localStorage.
    cleanup();
    const issuesView = await renderLoadedIssueList();

    // acme/widgets must still be expanded on the issues surface.
    expect(header(issuesView.container, "acme/widgets").getAttribute("aria-expanded")).toBe("true");
    // Seed data: widgets has 3 open issues; tools and GitLab add 1 each.
    expect(issueItems(issuesView.container)).toHaveLength(5);

    // The persisted state is keyed per surface.
    expect(JSON.parse(localStorage.getItem("middleman:collapsedRepos:pulls") ?? "[]")).toEqual(["acme/widgets"]);
    expect(localStorage.getItem("middleman:collapsedRepos:issues")).toBeNull();
  });

  it("collapse, expand, and persist acme/widgets", async () => {
    const first = await renderLoadedIssueList();

    // Default: 5 issues total (3 widgets + 1 tools + 1 GitLab).
    expect(issueItems(first.container)).toHaveLength(5);

    // Collapse widgets: 2 issues remain (tools#5 and GitLab group/project#11).
    await fireEvent.click(header(first.container, "acme/widgets"));
    expect(header(first.container, "acme/widgets").getAttribute("aria-expanded")).toBe("false");
    expect(header(first.container, "acme/widgets").querySelector(".repo-header__count")?.textContent).toBe("3");
    expect(issueItems(first.container)).toHaveLength(2);

    // Expand again: back to 5.
    await fireEvent.click(header(first.container, "acme/widgets"));
    expect(header(first.container, "acme/widgets").getAttribute("aria-expanded")).toBe("true");
    expect(issueItems(first.container)).toHaveLength(5);

    // Collapse again and verify persisted state in a fresh render with fresh
    // store instances (the e2e's "new page" equivalent).
    await fireEvent.click(header(first.container, "acme/widgets"));
    expect(JSON.parse(localStorage.getItem("middleman:collapsedRepos:issues") ?? "[]")).toEqual(["acme/widgets"]);
    cleanup();
    const second = await renderLoadedIssueList();
    expect(header(second.container, "acme/widgets").getAttribute("aria-expanded")).toBe("false");
    expect(issueItems(second.container)).toHaveLength(2);
  });
});
