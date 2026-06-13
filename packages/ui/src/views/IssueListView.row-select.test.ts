// Vitest conversion of frontend/tests/e2e-full/navigation.spec.ts
// "clicking an issue row opens the detail pane".
//
// Renders the REAL IssueListView with the real IssueList sidebar, real
// issues store, and the real IssueDetail pane, fed by a fake MiddlemanClient
// serving the seeded e2e fixture data. The Playwright spec's router
// round-trip (IssueList navigate() -> App passes selectedIssue back down) is
// modeled by rerendering with the issues store's real selection, exactly
// like App.svelte's `stores?.issues.getSelectedIssue()` lookup.
import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { Issue, IssueDetail } from "../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, SIDEBAR_KEY, STORES_KEY, UI_CONFIG_KEY } from "../context.js";
import { createCollapsedReposStore } from "../stores/collapsedRepos.svelte.js";
import { createGroupingStore } from "../stores/grouping.svelte.js";
import { createIssuesStore } from "../stores/issues.svelte.js";
import { seedGet } from "../testing/seedClient.js";
import type { MiddlemanClient } from "../types.js";
import IssueListView from "./IssueListView.svelte";

// Seed data (fetched live from the seeded e2e Go server): 5 open issues,
// the first being acme/widgets#10 "Widget rendering broken on Safari".
let issuesFixture: Issue[];
let issueDetailFixture: IssueDetail;

beforeAll(async () => {
  issuesFixture = await seedGet<Issue[]>("/api/v1/issues");
  issueDetailFixture = await seedGet<IssueDetail>("/api/v1/issues/github/acme/widgets/10");
});

type PathParams = { provider?: string; owner?: string; name?: string; number?: number };

function fakeClient(): MiddlemanClient {
  return {
    GET: async (path: string, opts?: { params?: { path?: PathParams } }) => {
      if (path === "/issues") {
        return { data: structuredClone(issuesFixture) };
      }
      if (path === "/issues/{provider}/{owner}/{name}/{number}") {
        const p = opts?.params?.path ?? {};
        if (p.provider === "github" && p.owner === "acme" && p.name === "widgets" && p.number === 10) {
          return { data: structuredClone(issueDetailFixture) };
        }
        return { error: { title: "unexpected issue detail request", detail: JSON.stringify(p) } };
      }
      return { error: { title: "unexpected request", detail: path } };
    },
    POST: async (path: string) => ({ error: { title: "unexpected request", detail: path } }),
  } as unknown as MiddlemanClient;
}

function renderIssueListView() {
  const grouping = createGroupingStore();
  const collapsedRepos = createCollapsedReposStore();
  const client = fakeClient();
  const issues = createIssuesStore({
    client,
    getGroupByRepo: () => grouping.getGroupByRepo(),
  });
  const navigate = vi.fn();

  const utils = render(IssueListView, {
    props: {
      selectedIssue: null,
      // The e2e server answered the post-load background sync; the fake
      // client doesn't, so skip the sync leg. The pane-visibility behavior
      // under test only depends on the initial detail GET.
      autoSyncDetail: false as const,
    },
    context: new Map<symbol, unknown>([
      [API_CLIENT_KEY, client],
      [
        STORES_KEY,
        {
          issues,
          grouping,
          collapsedRepos,
          sync: { getSyncState: () => null, onNextSyncComplete: vi.fn() },
          settings: {
            isSettingsLoaded: () => true,
            hasConfiguredRepos: () => true,
          },
          activity: { loadActivity: vi.fn() },
        },
      ],
      [NAVIGATE_KEY, navigate],
      [ACTIONS_KEY, { issue: [] }],
      [UI_CONFIG_KEY, { hideStar: true }],
      [
        SIDEBAR_KEY,
        {
          isEmbedded: () => true,
          isSidebarToggleEnabled: () => false,
          toggleSidebar: vi.fn(),
        },
      ],
    ]),
  });
  return { ...utils, issues, navigate };
}

describe("issue row selection (navigation.spec.ts conversion)", () => {
  beforeEach(() => {
    localStorage.clear();
    // jsdom does not implement scrollIntoView, which the issue row calls
    // when it becomes selected.
    Element.prototype.scrollIntoView = vi.fn();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    localStorage.clear();
  });

  it("clicking an issue row opens the detail pane", async () => {
    const { container, rerender, issues, navigate } = renderIssueListView();

    // List loads from the fixture; the detail pane is not showing initially.
    await waitFor(() => {
      expect(container.querySelectorAll(".issue-item").length).toBeGreaterThan(0);
    });
    expect(container.querySelector(".issue-detail")).toBeNull();
    expect(container.textContent).toContain("Select an issue");

    // First row is widgets#10, matching the seeded server's ordering.
    const firstItem = container.querySelector<HTMLElement>(".issue-item");
    expect(firstItem?.textContent).toContain("Widget rendering broken on Safari");

    await fireEvent.click(firstItem!);

    // The row click drove the real route builder...
    expect(navigate).toHaveBeenCalledWith("/issues/github/acme/widgets/10");
    // ...and the real store selection.
    const selected = issues.getSelectedIssue();
    expect(selected).toMatchObject({
      provider: "github",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 10,
    });

    // App reacts to the navigation by passing the selection back down.
    await rerender({ selectedIssue: selected });

    // The real IssueDetail pane loads the fixture detail and becomes visible.
    await waitFor(() => {
      expect(container.querySelector(".issue-detail")).not.toBeNull();
    });
    expect(container.querySelector(".issue-detail")?.textContent).toContain("Widget rendering broken on Safari");
  });
});
