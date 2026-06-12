// Vitest conversion of frontend/tests/e2e-full/navigation.spec.ts
// "clicking a PR row opens the detail pane".
//
// Renders the REAL PRListView with the real PullList sidebar, real
// pulls/detail stores, and the real PullDetail pane, fed by a fake
// MiddlemanClient serving the seeded e2e fixture data. The Playwright spec's
// router round-trip (PullList navigate() -> App passes selectedPR back down)
// is modeled by rerendering with the pulls store's real selection, exactly
// like App.svelte's `stores?.pulls.getSelectedPR()` fallback.
import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { PullRequest } from "../api/types.js";
import {
  ACTIONS_KEY,
  API_CLIENT_KEY,
  HOST_STATE_KEY,
  NAVIGATE_KEY,
  SIDEBAR_KEY,
  STORES_KEY,
  UI_CONFIG_KEY,
} from "../context.js";
import { createCollapsedReposStore } from "../stores/collapsedRepos.svelte.js";
import { createDetailStore } from "../stores/detail.svelte.js";
import { createGroupingStore } from "../stores/grouping.svelte.js";
import { createPullsStore } from "../stores/pulls.svelte.js";
import pullDetailFixtureJson from "../testing/e2e-fixtures/pull-widgets-1.json";
import pullsFixtureJson from "../testing/e2e-fixtures/pulls.json";
import type { MiddlemanClient } from "../types.js";
import PRListView from "./PRListView.svelte";

// Seed data (captured from the real seeded e2e Go server): 8 open PRs, the
// most recently active being acme/widgets#1 "Add widget caching layer".
const pullsFixture = pullsFixtureJson as unknown as PullRequest[];

type PathParams = { provider?: string; owner?: string; name?: string; number?: number };

function fakeClient(): MiddlemanClient {
  return {
    GET: async (path: string, opts?: { params?: { path?: PathParams } }) => {
      if (path === "/pulls") {
        return { data: structuredClone(pullsFixture) };
      }
      if (path === "/pulls/{provider}/{owner}/{name}/{number}") {
        const p = opts?.params?.path ?? {};
        if (p.provider === "github" && p.owner === "acme" && p.name === "widgets" && p.number === 1) {
          return { data: structuredClone(pullDetailFixtureJson) };
        }
        return { error: { title: "unexpected pull detail request", detail: JSON.stringify(p) } };
      }
      if (path === "/repo/{provider}/{owner}/{name}") {
        // Repo merge settings probed by PullDetail's merge button.
        return {
          data: {
            AllowSquashMerge: true,
            AllowMergeCommit: false,
            AllowRebaseMerge: false,
            ViewerCanMerge: true,
          },
        };
      }
      return { error: { title: "unexpected request", detail: path } };
    },
    POST: async (path: string) => ({ error: { title: "unexpected request", detail: path } }),
  } as unknown as MiddlemanClient;
}

function renderPRListView() {
  const grouping = createGroupingStore();
  const collapsedRepos = createCollapsedReposStore();
  const client = fakeClient();
  const pulls = createPullsStore({
    client,
    getGroupByRepo: () => grouping.getGroupByRepo(),
  });
  const detail = createDetailStore({ client });
  const navigate = vi.fn();

  const utils = render(PRListView, {
    props: {
      selectedPR: null,
      detailTab: "conversation" as const,
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
          pulls,
          detail,
          grouping,
          collapsedRepos,
          sync: { getSyncState: () => null, onNextSyncComplete: vi.fn() },
          settings: {
            isSettingsLoaded: () => true,
            hasConfiguredRepos: () => true,
          },
          activity: { loadActivity: vi.fn() },
          diff: { requestScrollToLine: vi.fn() },
        },
      ],
      [NAVIGATE_KEY, navigate],
      [ACTIONS_KEY, { pull: [] }],
      [HOST_STATE_KEY, {}],
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
  return { ...utils, pulls, navigate };
}

describe("PR row selection (navigation.spec.ts conversion)", () => {
  beforeEach(() => {
    localStorage.clear();
    // jsdom does not implement scrollIntoView, which PullItem calls when a
    // row becomes selected.
    Element.prototype.scrollIntoView = vi.fn();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    localStorage.clear();
  });

  it("clicking a PR row opens the detail pane", async () => {
    const { container, rerender, pulls, navigate } = renderPRListView();

    // List loads from the fixture; the detail pane is not showing initially.
    await waitFor(() => {
      expect(container.querySelectorAll(".pull-item").length).toBeGreaterThan(0);
    });
    expect(container.querySelector(".pull-detail")).toBeNull();
    expect(container.textContent).toContain("Select a PR");

    // First row is widgets#1, matching the seeded server's ordering.
    const firstItem = container.querySelector<HTMLElement>(".pull-item");
    expect(firstItem?.textContent).toContain("Add widget caching layer");

    await fireEvent.click(firstItem!);

    // The row click drove the real route builder...
    expect(navigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/1");
    // ...and the real store selection.
    const selected = pulls.getSelectedPR();
    expect(selected).toMatchObject({
      provider: "github",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 1,
    });

    // App reacts to the navigation by passing the selection back down.
    await rerender({ selectedPR: selected });

    // The real PullDetail pane loads the fixture detail and becomes visible.
    await waitFor(() => {
      expect(container.querySelector(".pull-detail")).not.toBeNull();
    });
    expect(container.querySelector(".pull-detail")?.textContent).toContain("Add widget caching layer");
  });
});
