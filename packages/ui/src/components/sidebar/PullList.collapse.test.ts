// Vitest component conversion of frontend/tests/e2e-full/collapsible-repos.spec.ts
// (PR-list cases). Uses the REAL grouping/collapsedRepos/pulls stores with a
// fake MiddlemanClient serving the seeded e2e fixture data, so collapse state,
// localStorage persistence, and grouping behavior are exercised for real.
import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { PullRequest } from "../../api/types.js";
import { ACTIONS_KEY, HOST_STATE_KEY, NAVIGATE_KEY, SIDEBAR_KEY, STORES_KEY } from "../../context.js";
import { createCollapsedReposStore } from "../../stores/collapsedRepos.svelte.js";
import { createGroupingStore } from "../../stores/grouping.svelte.js";
import { createPullsStore } from "../../stores/pulls.svelte.js";
import pullsFixtureJson from "../../testing/e2e-fixtures/pulls.json";
import type { MiddlemanClient } from "../../types.js";
import PullList from "./PullList.svelte";

// Seed data (captured from the real seeded e2e Go server):
// 8 open PRs -> acme/widgets #1, #2, #6, #7 and acme/tools #1, #10, #11, #12.
const pullsFixture = pullsFixtureJson as unknown as PullRequest[];

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

function header(container: HTMLElement, label: string): HTMLElement {
  const headers = [...container.querySelectorAll<HTMLElement>(".repo-header")];
  const match = headers.find((h) => h.querySelector(".repo-header__name")?.textContent === label);
  if (!match) {
    throw new Error(`no .repo-header with name "${label}"`);
  }
  return match;
}

function headerCount(container: HTMLElement, label: string): string {
  return header(container, label).querySelector(".repo-header__count")?.textContent ?? "";
}

async function renderLoadedPullList() {
  const utils = renderPullList();
  await waitFor(() => {
    expect(pullItems(utils.container).length).toBeGreaterThan(0);
  });
  return utils;
}

describe("collapsible repo groups (PR list)", () => {
  beforeEach(() => {
    // Mirrors the e2e beforeEach addInitScript: clear collapse state so each
    // test starts expanded.
    localStorage.removeItem("middleman:collapsedRepos:pulls");
    localStorage.removeItem("middleman:collapsedRepos:issues");
    // The e2e run started each test from a fresh browser profile; tests in
    // this file share one jsdom localStorage, so reset grouping keys too
    // (the status-grouping test below persists middleman:groupingMode).
    localStorage.removeItem("middleman:groupingMode");
    localStorage.removeItem("middleman:groupByRepo");
    localStorage.removeItem("middleman:hideOrgName");
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("default expanded shows every PR and both headers", async () => {
    const { container } = await renderLoadedPullList();

    expect(header(container, "acme/widgets").getAttribute("aria-expanded")).toBe("true");
    expect(header(container, "acme/tools").getAttribute("aria-expanded")).toBe("true");
    expect(pullItems(container)).toHaveLength(8);
  });

  it("collapsing acme/widgets hides its items, keeps header and count", async () => {
    const { container } = await renderLoadedPullList();

    await fireEvent.click(header(container, "acme/widgets"));

    expect(header(container, "acme/widgets").getAttribute("aria-expanded")).toBe("false");
    expect(headerCount(container, "acme/widgets")).toBe("4");
    // Only acme/tools' PRs remain visible (tools#1 + stack #10/#11/#12).
    expect(pullItems(container)).toHaveLength(4);
    // acme/tools stays expanded.
    expect(header(container, "acme/tools").getAttribute("aria-expanded")).toBe("true");
    // The real store persisted the collapse to localStorage.
    expect(JSON.parse(localStorage.getItem("middleman:collapsedRepos:pulls") ?? "[]")).toEqual(["acme/widgets"]);
  });

  it("expanding acme/widgets again restores its items", async () => {
    const { container } = await renderLoadedPullList();

    await fireEvent.click(header(container, "acme/widgets"));
    expect(pullItems(container)).toHaveLength(4);

    await fireEvent.click(header(container, "acme/widgets"));
    expect(header(container, "acme/widgets").getAttribute("aria-expanded")).toBe("true");
    expect(pullItems(container)).toHaveLength(8);
    expect(JSON.parse(localStorage.getItem("middleman:collapsedRepos:pulls") ?? "[]")).toEqual([]);
  });

  it("header is a native button so Enter and Space toggle collapse", async () => {
    // The e2e test pressed Enter/Space on the focused header. The component
    // has no keydown handler — it relies on native <button> activation
    // semantics, which jsdom does not synthesize from keyboard events. So we
    // assert the header IS a focusable native button (the browser guarantees
    // Enter/Space dispatch click on those) and verify the click activation
    // round-trips both ways.
    const { container } = await renderLoadedPullList();

    const widgets = header(container, "acme/widgets");
    expect(widgets.tagName).toBe("BUTTON");
    expect(widgets.getAttribute("type")).toBe("button");
    widgets.focus();
    expect(document.activeElement).toBe(widgets);

    await fireEvent.click(widgets);
    expect(header(container, "acme/widgets").getAttribute("aria-expanded")).toBe("false");
    expect(pullItems(container)).toHaveLength(4);

    await fireEvent.click(header(container, "acme/widgets"));
    expect(header(container, "acme/widgets").getAttribute("aria-expanded")).toBe("true");
    expect(pullItems(container)).toHaveLength(8);
  });

  it("status groups collapse like repo groups", async () => {
    const { container } = await renderLoadedPullList();

    const statusBtn = [...container.querySelectorAll<HTMLElement>(".group-btn")].find(
      (b) => b.textContent?.trim() === "Status",
    );
    expect(statusBtn).toBeDefined();
    await fireEvent.click(statusBtn!);

    // All 8 seed PRs are open with no kanban status -> single "New" group.
    expect(localStorage.getItem("middleman:groupingMode")).toBe("byWorkflow");
    expect(header(container, "New").getAttribute("aria-expanded")).toBe("true");

    await fireEvent.click(header(container, "New"));
    expect(header(container, "New").getAttribute("aria-expanded")).toBe("false");
    expect(headerCount(container, "New")).toBe("8");
    expect(pullItems(container)).toHaveLength(0);

    await fireEvent.click(header(container, "New"));
    expect(header(container, "New").getAttribute("aria-expanded")).toBe("true");
    expect(pullItems(container)).toHaveLength(8);
  });

  it("collapse state persists across reload", async () => {
    const first = await renderLoadedPullList();
    await fireEvent.click(header(first.container, "acme/widgets"));
    expect(header(first.container, "acme/widgets").getAttribute("aria-expanded")).toBe("false");

    // "Reload": unmount everything and re-render with FRESH store instances
    // that re-read the same localStorage, like a new page would.
    cleanup();
    const second = await renderLoadedPullList();

    expect(header(second.container, "acme/widgets").getAttribute("aria-expanded")).toBe("false");
    expect(pullItems(second.container)).toHaveLength(4);
  });
});
