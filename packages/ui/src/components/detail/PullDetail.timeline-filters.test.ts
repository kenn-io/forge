// Vitest component conversion of frontend/tests/e2e-full/pr-timeline-filters.spec.ts.
// Renders the real PullDetail (which owns the PRTimelineFilter dropdown, the
// localStorage persistence, and the filtered EventTimeline) against detail
// fixtures captured from the seeded e2e Go server, so filter interactions,
// force-push ordering, and persistence are exercised exactly as a user sees
// them — only the detail store's network fetch is replaced by the fixture.
import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { PullDetail } from "../../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, STORES_KEY, UI_CONFIG_KEY } from "../../context.js";
import toolsPull2Json from "../../testing/e2e-fixtures/pull-tools-2.json";
import widgetsPull1Json from "../../testing/e2e-fixtures/pull-widgets-1.json";
import widgetsPull2Json from "../../testing/e2e-fixtures/pull-widgets-2.json";
import PullDetailComponent from "./PullDetail.svelte";

const storageKey = "middleman-pr-timeline-filter";

// jsdom has no Web Animations API; Svelte's transition:slide on timeline
// thread replies calls element.animate when filters add/remove rows. The stub
// completes every animation on the next microtask so outros finish and
// filtered rows actually leave the DOM.
type AnimatableElement = HTMLElement & { animate?: (...args: unknown[]) => unknown };
let originalAnimate: ((...args: unknown[]) => unknown) | undefined;
let animatePolyfilled = false;

class AnimationStub {
  onfinish: (() => void) | null = null;
  oncancel: (() => void) | null = null;
  finished: Promise<AnimationStub>;
  playState = "finished";
  pending = false;
  currentTime = 0;
  constructor() {
    this.finished = Promise.resolve(this);
    queueMicrotask(() => this.onfinish?.());
  }
  cancel(): void {
    this.oncancel?.();
  }
  finish(): void {
    this.onfinish?.();
  }
  play(): void {}
  pause(): void {}
  reverse(): void {}
}

beforeAll(() => {
  const proto = Element.prototype as unknown as AnimatableElement;
  originalAnimate = proto.animate;
  if (typeof proto.animate !== "function") {
    proto.animate = () => new AnimationStub();
    animatePolyfilled = true;
  }
});

afterAll(() => {
  if (animatePolyfilled) {
    const proto = Element.prototype as unknown as AnimatableElement;
    if (originalAnimate) {
      proto.animate = originalAnimate;
    } else {
      delete proto.animate;
    }
  }
});

const widgetsPull1 = widgetsPull1Json as unknown as PullDetail;
const widgetsPull2 = widgetsPull2Json as unknown as PullDetail;
const toolsPull2 = toolsPull2Json as unknown as PullDetail;

function renderPullDetail(fixture: PullDetail) {
  const detail = structuredClone(fixture);
  const detailStore = {
    loadDetail: vi.fn(async () => undefined),
    startDetailPolling: vi.fn(),
    stopDetailPolling: vi.fn(),
    getDetail: () => detail,
    isDetailLoading: () => false,
    getDetailError: () => null,
    isStaleRefreshing: () => false,
    isDetailSyncing: () => false,
    getDetailLoaded: () => true,
    updateKanbanState: vi.fn(),
    toggleDetailPRStar: vi.fn(),
    updatePRContent: vi.fn(),
    refreshPendingCI: vi.fn(async () => undefined),
    editComment: vi.fn(),
  };
  const rendered = render(PullDetailComponent, {
    props: {
      owner: detail.repo_owner,
      name: detail.repo_name,
      number: detail.merge_request.Number,
      provider: detail.repo.provider,
      platformHost: detail.repo.platform_host,
      repoPath: detail.repo.repo_path,
      hideWorkspaceAction: true,
    },
    context: new Map<symbol, unknown>([
      [
        API_CLIENT_KEY,
        {
          GET: vi.fn(async () => ({
            data: {
              AllowSquashMerge: false,
              AllowMergeCommit: false,
              AllowRebaseMerge: false,
              ViewerCanMerge: false,
            },
          })),
        },
      ],
      [
        STORES_KEY,
        {
          detail: detailStore,
          pulls: { loadPulls: vi.fn() },
          activity: { loadActivity: vi.fn() },
        },
      ],
      [ACTIONS_KEY, { pull: [] }],
      [UI_CONFIG_KEY, { hideStar: true }],
      [NAVIGATE_KEY, vi.fn()],
    ]),
  });
  return rendered;
}

function timelineEl(): HTMLElement {
  const timeline = document.querySelector<HTMLElement>(".timeline");
  if (timeline === null) {
    throw new Error("timeline element not rendered");
  }
  return timeline;
}

function filterButton(): HTMLButtonElement {
  const button = document.querySelector<HTMLButtonElement>('button[title="Filter PR activity"]');
  if (button === null) {
    throw new Error("timeline filter trigger not rendered");
  }
  return button;
}

async function openTimelineFilters(): Promise<void> {
  await fireEvent.click(filterButton());
  expect(document.querySelector(".filter-dropdown")).not.toBeNull();
}

function cacheCommitRow(): HTMLElement {
  const rows = [...document.querySelectorAll<HTMLElement>(".event--compact")];
  const row = rows.find((candidate) => candidate.textContent?.includes("abc1111"));
  if (row === undefined) {
    throw new Error('no .event--compact row containing "abc1111"');
  }
  return row;
}

function expectTimelineTextOrder(labels: string[]): void {
  const text = timelineEl().textContent ?? "";
  const positions = labels.map((label) => text.indexOf(label));
  expect(positions.every((position) => position >= 0)).toBe(true);
  expect(positions).toEqual([...positions].sort((a, b) => a - b));
}

describe("PR timeline filters", () => {
  beforeEach(() => {
    localStorage.removeItem(storageKey);
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders seeded commit and system timeline events", () => {
    renderPullDetail(widgetsPull1);

    const timeline = within(timelineEl());
    // Seeded commit content (body shown because commit details default on).
    expect(timelineEl().textContent).toContain("feat: add cache store");
    expect(timelineEl().textContent).toContain("Cache entries now expire");

    expect(timeline.getAllByText("Force-pushed")).toHaveLength(2);
    expect(timeline.getByText("abc4444 -> def5555")).toBeTruthy();
    expect(timeline.getByText("abc9999 -> def7777")).toBeTruthy();
    expect(timeline.getAllByText("Referenced")).toHaveLength(3);
    expect(timeline.getByText("Widget rendering broken on Safari")).toBeTruthy();
    expect(timeline.getByText("Title changed")).toBeTruthy();
    expect(timeline.getByText('"Add widget cache" -> "Add widget caching layer"')).toBeTruthy();
    expect(timeline.getByText("Base changed")).toBeTruthy();
    expect(timeline.getByText("develop -> main")).toBeTruthy();
  });

  it("renders merged lifecycle transitions as one purple row", () => {
    renderPullDetail(toolsPull2);

    const compactRows = [...document.querySelectorAll<HTMLElement>(".event--compact")];
    const mergedRows = compactRows.filter((row) => row.textContent?.includes("Merged"));
    expect(mergedRows).toHaveLength(1);
    const merged = mergedRows[0]!;
    expect(merged.textContent).toContain("alice");
    expect(merged.textContent).toContain("merged this");

    const label = within(merged).getByText("Merged");
    expect(label.classList.contains("event-type")).toBe(true);
    expect(label.getAttribute("style")).toContain("var(--accent-purple)");
    expect(merged.querySelector(".dot")?.getAttribute("style")).toContain("var(--accent-purple)");

    expect(compactRows.filter((row) => row.textContent?.includes("Closed"))).toHaveLength(0);
  });

  it("orders force-push commit generations through the seeded timeline", () => {
    renderPullDetail(widgetsPull1);

    expectTimelineTextOrder([
      "Base changed",
      "chore: tune cache eviction metrics",
      "Title changed",
      "fix: finish cache rebase after follow-up force push",
      "abc9999 -> def7777",
      "Same timestamp reviewer note between force-push IDs.",
      "fix: guard nil cache after rebase",
      "abc4444 -> def5555",
      "fix: guard nil cache before rebase",
    ]);
  });

  it("orders fresh-import force-push commits without the old anchor commit", () => {
    renderPullDetail(widgetsPull2);

    expectTimelineTextOrder([
      "fix: guard widget race after import",
      "test: reproduce widget race after import",
      "2222aaa -> 2222ccc",
    ]);
  });

  it("keeps commit rows while hiding and restoring system event buckets", async () => {
    renderPullDetail(widgetsPull1);
    await openTimelineFilters();

    await fireEvent.click(screen.getByRole("button", { name: "Commit details" }));
    let commitRow = cacheCommitRow();
    expect(commitRow.querySelector(".commit-title")?.textContent?.trim()).toBe("feat: add cache store");
    expect(commitRow.querySelectorAll(".commit-body-details")).toHaveLength(0);

    await fireEvent.click(screen.getByRole("button", { name: "Commit details" }));
    commitRow = cacheCommitRow();
    expect(commitRow.querySelectorAll(".commit-title")).toHaveLength(0);
    expect(commitRow.querySelector(".commit-body-details")?.textContent).toContain("Cache entries now expire");

    await fireEvent.click(screen.getByRole("button", { name: "Events" }));
    expect(screen.queryByText("Widget rendering broken on Safari")).toBeNull();
    expect(screen.queryByText('"Add widget cache" -> "Add widget caching layer"')).toBeNull();
    expect(screen.queryByText("develop -> main")).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Events" }));
    expect(screen.getByText("Widget rendering broken on Safari")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Force pushes" }));
    expect(screen.queryByText("abc4444 -> def5555")).toBeNull();
    expect(screen.queryByText("abc9999 -> def7777")).toBeNull();
    // Hidden force pushes still order the visible commit generations.
    expectTimelineTextOrder([
      "fix: finish cache rebase after follow-up force push",
      "fix: guard nil cache after rebase",
      "fix: guard nil cache before rebase",
    ]);
    await fireEvent.click(screen.getByRole("button", { name: "Force pushes" }));
    expect(screen.getByText("abc4444 -> def5555")).toBeTruthy();
  });

  it("persists timeline filter preferences in localStorage", async () => {
    renderPullDetail(widgetsPull1);
    await openTimelineFilters();

    await fireEvent.click(screen.getByRole("button", { name: "Events" }));
    expect(screen.queryByText("Widget rendering broken on Safari")).toBeNull();
    expect(filterButton().textContent).toContain("1");
    expect(localStorage.getItem(storageKey)).toContain('"showEvents":false');

    // "Reload": unmount and render a fresh PullDetail that re-reads the
    // persisted filter state from localStorage like a new page would.
    cleanup();
    renderPullDetail(widgetsPull1);

    expect(screen.queryByText("Widget rendering broken on Safari")).toBeNull();
    expect(filterButton().textContent).toContain("1");
  });

  it("keeps commit rows when other event buckets are hidden", async () => {
    renderPullDetail(widgetsPull1);
    await openTimelineFilters();

    await fireEvent.click(screen.getByRole("button", { name: "Messages" }));
    await fireEvent.click(screen.getByRole("button", { name: "Commit details" }));
    await fireEvent.click(screen.getByRole("button", { name: "Events" }));
    await fireEvent.click(screen.getByRole("button", { name: "Force pushes" }));

    const commitRow = cacheCommitRow();
    expect(commitRow.querySelector(".commit-title")?.textContent?.trim()).toBe("feat: add cache store");
    expect(commitRow.querySelectorAll(".commit-body-details")).toHaveLength(0);
    expect(screen.queryByText("No activity matches the current filters")).toBeNull();
  });
});
