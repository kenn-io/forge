// Vitest component conversion of frontend/tests/e2e-full/grouping-toggle.spec.ts
// (PR-list cases; activity cases live elsewhere). Uses the REAL grouping and
// collapsedRepos stores plus a real pulls store fed by a fake MiddlemanClient
// serving the seeded e2e fixture data, so toggle behavior and localStorage
// persistence are exercised for real.
import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { PullRequest } from "../../api/types.js";
import { ACTIONS_KEY, HOST_STATE_KEY, NAVIGATE_KEY, SIDEBAR_KEY, STORES_KEY } from "../../context.js";
import { createCollapsedReposStore } from "../../stores/collapsedRepos.svelte.js";
import { createGroupingStore } from "../../stores/grouping.svelte.js";
import { createPullsStore } from "../../stores/pulls.svelte.js";
import { seedGet } from "../../testing/seedClient.js";
import type { MiddlemanClient } from "../../types.js";
import PullList from "./PullList.svelte";

// Seed data (fetched live from the seeded e2e Go server):
// 8 open PRs in fixture/flat order: widgets#1, widgets#7, widgets#6,
// tools#1, widgets#2, tools#12, tools#11, tools#10.
let pullsFixture: PullRequest[];

beforeAll(async () => {
  pullsFixture = await seedGet<PullRequest[]>("/api/v1/pulls");
});

function fakeClient(): MiddlemanClient {
  return {
    GET: async (path: string) => {
      if (path === "/pulls") {
        return { data: structuredClone(pullsFixture) };
      }
      return { error: { title: "unexpected request", detail: path } };
    },
  } as unknown as MiddlemanClient;
}

function renderPullList() {
  const grouping = createGroupingStore();
  const collapsedRepos = createCollapsedReposStore();
  const pulls = createPullsStore({
    client: fakeClient(),
    // Same wiring the app uses: keyboard next/prev order follows grouping.
    getGroupByRepo: () => grouping.getGroupByRepo(),
  });
  const utils = render(PullList, {
    props: { sidebarWidth: 600, showSelectedDiffSidebar: false },
    context: new Map<symbol, unknown>([
      [
        STORES_KEY,
        {
          pulls,
          grouping,
          collapsedRepos,
          sync: { getSyncState: () => null, onNextSyncComplete: vi.fn() },
          settings: {
            isSettingsLoaded: () => true,
            hasConfiguredRepos: () => true,
          },
        },
      ],
      [NAVIGATE_KEY, vi.fn()],
      [ACTIONS_KEY, {}],
      [HOST_STATE_KEY, {}],
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
  return { ...utils, pulls, grouping, collapsedRepos };
}

function pullItems(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(".pull-item")];
}

function repoHeaders(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(".repo-header")];
}

function repoChips(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(".repo-chip")];
}

function groupBtn(container: HTMLElement, label: string): HTMLElement {
  const btn = [...container.querySelectorAll<HTMLElement>(".group-btn")].find((b) => b.textContent?.trim() === label);
  if (!btn) {
    throw new Error(`no .group-btn with label "${label}"`);
  }
  return btn;
}

function selectedMeta(container: HTMLElement): string | null | undefined {
  const selected = container.querySelectorAll<HTMLElement>(".pull-item.selected");
  expect(selected).toHaveLength(1);
  return selected[0]?.querySelector(".meta-left")?.textContent?.trim();
}

async function renderLoadedPullList() {
  const utils = renderPullList();
  await waitFor(() => {
    expect(pullItems(utils.container).length).toBeGreaterThan(0);
  });
  return utils;
}

describe("grouping toggle (PR list)", () => {
  beforeEach(() => {
    // Mirrors the e2e beforeEach addInitScript: clear persisted grouping so
    // tests start in default grouped mode.
    localStorage.removeItem("middleman:groupingMode");
    localStorage.removeItem("middleman:groupByRepo");
    localStorage.removeItem("middleman:hideOrgName");
    // jsdom localStorage persists across tests in this file; keep collapse
    // state from leaking between tests as well.
    localStorage.removeItem("middleman:collapsedRepos:pulls");
    localStorage.removeItem("middleman:collapsedRepos:issues");
    // PullItem scrolls the selected row into view; jsdom has no layout.
    Element.prototype.scrollIntoView = vi.fn();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("PR list defaults to grouped with repo headers", async () => {
    const { container } = await renderLoadedPullList();

    expect(repoHeaders(container).length).toBeGreaterThan(0);
    // No repo badges visible in grouped mode.
    expect(repoChips(container)).toHaveLength(0);
  });

  it("PR list ungrouped shows repo badges and no headers", async () => {
    const { container } = await renderLoadedPullList();

    // Click "All" in the group toggle.
    await fireEvent.click(groupBtn(container, "All"));

    // Repo headers disappear; one badge per PR appears.
    expect(repoHeaders(container)).toHaveLength(0);
    const items = pullItems(container);
    expect(items.length).toBeGreaterThan(0);
    expect(repoChips(container)).toHaveLength(items.length);
  });

  it("toggle persists across page reload", async () => {
    const first = await renderLoadedPullList();
    await fireEvent.click(groupBtn(first.container, "All"));
    expect(repoHeaders(first.container)).toHaveLength(0);
    // The real grouping store persisted the mode.
    expect(localStorage.getItem("middleman:groupingMode")).toBe("flat");

    // "Reload": unmount and re-render with FRESH store instances that
    // re-read the same localStorage, like a new page would.
    cleanup();
    const second = await renderLoadedPullList();

    expect(repoHeaders(second.container)).toHaveLength(0);
    expect(repoChips(second.container).length).toBeGreaterThan(0);
  });

  it("j/k navigation follows flat order in ungrouped mode", async () => {
    // The app's "j" binding calls pulls.selectNextPR() (see
    // frontend/src/lib/stores/keyboard/actions.ts go.next); we drive that
    // same store operation and assert the rendered selection.
    const { container, pulls } = await renderLoadedPullList();
    await fireEvent.click(groupBtn(container, "All"));
    expect(repoHeaders(container)).toHaveLength(0);

    // Capture the visible flat order of items.
    const items = pullItems(container);
    const firstVisibleMeta = items[0]?.querySelector(".meta-left")?.textContent?.trim();
    const secondVisibleMeta = items[1]?.querySelector(".meta-left")?.textContent?.trim();
    // Anchor to the fixture's actual flat order: widgets#1 then widgets#7.
    expect(firstVisibleMeta).toContain("#1");
    expect(secondVisibleMeta).toContain("#7");

    // First "j": selects the first visible item.
    pulls.selectNextPR();
    await waitFor(() => {
      expect(selectedMeta(container)).toBe(firstVisibleMeta);
    });

    // Second "j": selects the second visible item.
    pulls.selectNextPR();
    await waitFor(() => {
      expect(selectedMeta(container)).toBe(secondVisibleMeta);
    });
  });
});
