// Guards the compact sidebar title-row layout: the title gives up its space
// first, down to a small floor, so chip text is never cut. Chips that still
// cannot fit whole move into the +N badge. .title
// has overflow: hidden, so a label cluster that refused to shrink would
// silently clip the trailing #number instead of visibly overflowing.
//
// Mounts PullItem directly (no full App) at a normal sidebar width and at the
// two widths where the title row is tightest: the 200px minimum sidebar width,
// and a narrow phone card inside a .mobile-main-classed wrapper (the mobile
// card layout scales the title up and wraps it to two lines, but the number
// must still stay put).

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";

import type { PullRequest } from "./lib/api/types.js";
import { HOST_STATE_KEY, STORES_KEY } from "./lib/context.js";
import PullItem from "./lib/components/sidebar/PullItem.svelte";
import "./app.css";

const SHORT_LABELS = [
  { name: "bug", color: "d73a4a" },
  { name: "ux", color: "0075ca" },
];

function mkPR(overrides: Record<string, unknown> = {}): PullRequest {
  return {
    Number: 1234,
    Title: "Refactor the entire synchronization pipeline to support multiple concurrent providers",
    Author: "alice",
    State: "open",
    IsDraft: false,
    KanbanStatus: "new",
    CIStatus: "",
    CIChecksJSON: "",
    MergeableState: "clean",
    ReviewDecision: "",
    LastActivityAt: new Date().toISOString(),
    PlatformExternalID: "ext-1",
    repo_owner: "o",
    repo_name: "n",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "o",
      name: "n",
      repo_path: "o/n",
    },
    worktree_links: [],
    Starred: false,
    labels: [
      { name: "component-integration-testing-suite", color: "d73a4a" },
      { name: "documentation-and-release-notes-update", color: "0075ca" },
      { name: "chore", color: "008672" },
    ],
    ...overrides,
  } as unknown as PullRequest;
}

interface MountedItem {
  wrapper: HTMLElement;
  update: (changes: Record<string, unknown>) => Promise<void>;
  unmount: () => void;
}

async function mountAt(
  widthPx: number,
  mobileMain: boolean,
  overrides: Record<string, unknown> = {},
  settings: Record<string, unknown> = { show_agent_status_in_lists: false },
): Promise<MountedItem> {
  const wrapper = document.createElement("div");
  wrapper.style.width = `${widthPx}px`;
  wrapper.style.boxSizing = "border-box";
  if (mobileMain) wrapper.classList.add("mobile-main");
  document.body.appendChild(wrapper);

  const props = {
    pr: mkPR(overrides),
    selected: false,
    showRepo: false,
    repoLabel: "o/n",
    onclick: () => {},
  };
  const { rerender, unmount } = await render(PullItem, {
    target: wrapper,
    props,
    context: new Map<symbol, unknown>([
      [
        STORES_KEY,
        {
          settings: { getWorkspaceSettings: () => settings },
          activity: { getUseWorkspaceActivityForRecency: () => false },
          pulls: { togglePRStar: vi.fn() },
        },
      ],
      [HOST_STATE_KEY, {}],
    ]),
  });

  return {
    wrapper,
    update: (changes) => rerender({ ...props, pr: mkPR({ ...overrides, ...changes }) }),
    unmount: () => {
      unmount();
      wrapper.remove();
    },
  };
}

describe("PullItem compact label row at narrow widths", () => {
  let mounted: MountedItem | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
    cleanup();
    document.documentElement.style.fontSize = "";
  });

  function nextFrame(): Promise<void> {
    return new Promise((resolve) => requestAnimationFrame(() => resolve()));
  }

  function assertTitleRowFits(): void {
    const item = document.querySelector<HTMLElement>(".pull-item")!;
    const numberEl = document.querySelector<HTMLElement>(".title .item-number")!;
    const titleTextEl = document.querySelector<HTMLElement>(".title .title-text")!;
    const labelRowEl = document.querySelector<HTMLElement>(".title .label-row")!;

    const itemRect = item.getBoundingClientRect();
    const numberRect = numberEl.getBoundingClientRect();
    const titleTextRect = titleTextEl.getBoundingClientRect();
    const labelRowRect = labelRowEl.getBoundingClientRect();

    // The item number is small and identity-critical: it must never be
    // clipped by the row's overflow: hidden.
    expect(numberRect.width).toBeGreaterThan(0);
    expect(numberRect.right).toBeLessThanOrEqual(itemRect.right + 0.5);

    // The title yields to labels first but keeps a small floor: it must never
    // collapse to zero width even when the label cluster is long.
    expect(titleTextRect.width).toBeGreaterThan(0);

    // The label cluster must not push content outside the row's box.
    expect(labelRowRect.left).toBeGreaterThanOrEqual(itemRect.left - 0.5);
    expect(labelRowRect.right).toBeLessThanOrEqual(itemRect.right + 0.5);
  }

  function shownChips(): HTMLElement[] {
    return [...document.querySelectorAll<HTMLElement>(".title .kit-color-label")].filter(
      (chip) => getComputedStyle(chip).visibility === "visible",
    );
  }

  function assertChipsShownWhole(names: string[]): void {
    const clusterRect = document.querySelector<HTMLElement>(".title .label-row")!.getBoundingClientRect();
    const chips = shownChips();
    expect(chips.map((chip) => chip.textContent)).toEqual(names);
    for (const chip of chips) {
      expect(chip.scrollWidth).toBeLessThanOrEqual(chip.clientWidth);
      expect(chip.getBoundingClientRect().right).toBeLessThanOrEqual(clusterRect.right + 0.5);
    }
  }

  function badgeText(): string | undefined {
    return document.querySelector<HTMLElement>(".title .label-more")?.textContent ?? undefined;
  }

  it("keeps the item number visible and the title non-zero at the 200px minimum sidebar width", async () => {
    mounted = await mountAt(200, false);
    await vi.waitFor(() => expect(badgeText()).toBe("+3"));
    assertTitleRowFits();
  });

  it("keeps the item number visible and the title non-zero on a narrow phone card", async () => {
    mounted = await mountAt(360, true);
    assertTitleRowFits();
  });

  it("shows short labels whole beside a long title at a normal sidebar width", async () => {
    mounted = await mountAt(340, false, { labels: SHORT_LABELS });
    assertChipsShownWhole(["bug", "ux"]);
    expect(badgeText()).toBeUndefined();
    assertTitleRowFits();
  });

  it("shows short labels whole at the 200px minimum sidebar width", async () => {
    mounted = await mountAt(200, false, { labels: SHORT_LABELS });
    assertChipsShownWhole(["bug", "ux"]);
    assertTitleRowFits();
  });

  it("shows a long label in full at a normal sidebar width, with the title giving way", async () => {
    mounted = await mountAt(340, false, { labels: [{ name: "component-integration-testing-suite", color: "d73a4a" }] });
    await vi.waitFor(() => assertChipsShownWhole(["component-integration-testing-suite"]));
    expect(badgeText()).toBeUndefined();
    assertTitleRowFits();
  });

  it("moves labels that cannot fit whole into the overflow badge and restores them with room", async () => {
    mounted = await mountAt(200, false);
    await vi.waitFor(() => expect(badgeText()).toBe("+3"));
    assertChipsShownWhole([]);

    mounted.wrapper.style.width = "600px";
    await vi.waitFor(() => expect(badgeText()).toBe("+1"));
    assertChipsShownWhole(["component-integration-testing-suite", "documentation-and-release-notes-update"]);
  });

  it("still shows a short label that fits when the label before it does not", async () => {
    mounted = await mountAt(200, false, {
      labels: [{ name: "component-integration-testing-suite", color: "d73a4a" }, SHORT_LABELS[0]],
    });
    await vi.waitFor(() => expect(badgeText()).toBe("+1"));
    assertChipsShownWhole(["bug"]);
    assertTitleRowFits();
  });

  it("fits again when the row's labels change", async () => {
    mounted = await mountAt(200, false);
    await vi.waitFor(() => expect(badgeText()).toBe("+3"));

    await mounted.update({ labels: SHORT_LABELS });
    await vi.waitFor(() => expect(badgeText()).toBeUndefined());
    assertChipsShownWhole(["bug", "ux"]);
  });

  it("moves a short label into the badge when the row has no room for it whole", async () => {
    mounted = await mountAt(
      200,
      false,
      { labels: SHORT_LABELS.slice(0, 1), workspace: { agent_state: "approval" }, Number: 123456 },
      { show_agent_status_in_lists: true },
    );
    await vi.waitFor(() => expect(badgeText()).toBe("+1"));
    expect(shownChips()).toEqual([]);
    assertTitleRowFits();
  });

  // The same labels array, as a list refresh keeps it when only the agent
  // state changes, so only the freed space can trigger the fit.
  it("shows a hidden label once the agent indicator beside it goes away", async () => {
    const labels = SHORT_LABELS.slice(0, 1);
    mounted = await mountAt(
      200,
      false,
      { labels, workspace: { agent_state: "approval" }, Number: 123456 },
      { show_agent_status_in_lists: true },
    );
    await vi.waitFor(() => expect(badgeText()).toBe("+1"));

    await mounted.update({ workspace: undefined });
    await vi.waitFor(() => expect(badgeText()).toBeUndefined());
    assertChipsShownWhole(["bug"]);
  });

  // An enlarged browser default font makes the rem-sized title floor wider
  // than the room the status and number leave.
  it("keeps the item number visible with an enlarged font at the 200px minimum sidebar width", async () => {
    document.documentElement.style.fontSize = "20px";
    mounted = await mountAt(
      200,
      false,
      { labels: SHORT_LABELS.slice(0, 1), workspace: { agent_state: "approval" }, Number: 123456 },
      { show_agent_status_in_lists: true },
    );
    await vi.waitFor(() => expect(badgeText()).toBe("+1"));
    assertTitleRowFits();
  });

  // A two-digit count at an enlarged font renders a badge wider than a fixed
  // allowance would assume. Sweeping the width crosses every fitting boundary,
  // starting where the row holds the title's floor beside the badge.
  it("never cuts a chip or the badge across row widths with an enlarged font", async () => {
    document.documentElement.style.fontSize = "32px";
    const labels = Array.from({ length: 12 }, (_, index) => ({ name: `area-${index}`, color: "0075ca" }));
    mounted = await mountAt(320, false, { labels });
    await vi.waitFor(() => expect(badgeText()).toBeDefined());

    for (let widthPx = 320; widthPx <= 720; widthPx += 4) {
      mounted.wrapper.style.width = `${widthPx}px`;
      await nextFrame();
      await nextFrame();
      const clusterRect = document.querySelector<HTMLElement>(".title .label-row")!.getBoundingClientRect();
      const badge = document.querySelector<HTMLElement>(".title .label-more")!;
      expect(badge.getBoundingClientRect().right, `badge at ${widthPx}px`).toBeLessThanOrEqual(clusterRect.right + 0.5);
      for (const chip of shownChips()) {
        expect(chip.scrollWidth, `chip at ${widthPx}px`).toBeLessThanOrEqual(chip.clientWidth);
        expect(chip.getBoundingClientRect().right, `chip at ${widthPx}px`).toBeLessThanOrEqual(clusterRect.right + 0.5);
      }
    }
  }, 60_000);
});
