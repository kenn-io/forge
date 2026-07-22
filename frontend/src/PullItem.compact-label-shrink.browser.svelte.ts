// Guards the compact sidebar title-row layout invariant from review: with two
// long label pills plus an overflow badge, the compact label cluster (a
// nowrap flex item on the title line) must yield space to the title and the
// item number rather than push the number off the visible row. .title has
// overflow: hidden, so a non-shrinking label cluster can silently clip the
// trailing #number instead of visibly overflowing.
//
// Mounts PullItem directly (no full App) at the two widths where the title
// row is tightest: the 200px minimum sidebar width, and a narrow phone card
// inside a .mobile-main-classed wrapper (the mobile card layout scales the
// title up and wraps it to two lines, but the number must still stay put).

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";

import type { PullRequest } from "../../packages/ui/src/api/types.js";
import { HOST_STATE_KEY, STORES_KEY } from "../../packages/ui/src/context.js";
import PullItem from "../../packages/ui/src/components/sidebar/PullItem.svelte";

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
  unmount: () => void;
}

function mountAt(widthPx: number, mobileMain: boolean): MountedItem {
  const wrapper = document.createElement("div");
  wrapper.style.width = `${widthPx}px`;
  wrapper.style.boxSizing = "border-box";
  if (mobileMain) wrapper.classList.add("mobile-main");
  document.body.appendChild(wrapper);

  const { unmount } = render(PullItem, {
    target: wrapper,
    props: {
      pr: mkPR(),
      selected: false,
      showRepo: false,
      repoLabel: "o/n",
      onclick: () => {},
    },
    context: new Map<symbol, unknown>([
      [STORES_KEY, { pulls: { togglePRStar: vi.fn() } }],
      [HOST_STATE_KEY, {}],
    ]),
  });

  return {
    wrapper,
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
  });

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

    // The title keeps space in preference to labels: it must never collapse
    // to zero width even when the label cluster is long.
    expect(titleTextRect.width).toBeGreaterThan(0);

    // The label cluster is allowed to shrink/clip, but must not push content
    // outside the row's box.
    expect(labelRowRect.left).toBeGreaterThanOrEqual(itemRect.left - 0.5);
    expect(labelRowRect.right).toBeLessThanOrEqual(itemRect.right + 0.5);
  }

  it("keeps the item number visible and the title non-zero at the 200px minimum sidebar width", () => {
    mounted = mountAt(200, false);
    assertTitleRowFits();
  });

  it("keeps the item number visible and the title non-zero on a narrow phone card", () => {
    mounted = mountAt(360, true);
    assertTitleRowFits();
  });
});
