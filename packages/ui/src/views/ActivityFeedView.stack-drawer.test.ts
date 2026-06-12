// Vitest component conversion of "stack member navigation updates the
// activity drawer with full-stack data" from
// frontend/tests/e2e-full/stack-status.spec.ts.
//
// Renders the real (uncontrolled) ActivityFeedView: clicking the seeded
// acme/tools#11 activity row opens the drawer, the drawer's PullDetail loads
// tools#11 through a REAL detail store backed by a fake client serving the
// captured e2e fixtures, and clicking stack member #10 routes through
// StackStatus.navigateToMember -> PullDetail -> PRListView ->
// ActivityFeedView.handleStackMemberNavigate, which swaps the drawer to #10
// and reports the new drawer item via onDrawerItemChange.
//
// Dropped from the original: the ?selected=pr:10&repo_path=acme%2Ftools URL
// assertions — App.svelte owns writing the drawer ref to the URL; this test
// asserts the onDrawerItemChange payload App uses to do that write.
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vite-plus/test";
import type { ActivityItem, PullDetail } from "../api/types.js";
import {
  ACTIONS_KEY,
  API_CLIENT_KEY,
  HOST_STATE_KEY,
  NAVIGATE_KEY,
  SIDEBAR_KEY,
  STORES_KEY,
  UI_CONFIG_KEY,
} from "../context.js";
import { createDetailStore } from "../stores/detail.svelte.js";
import activityJson from "../testing/e2e-fixtures/activity.json";
import stackTools10Json from "../testing/e2e-fixtures/stack-tools-10.json";
import stackTools11Json from "../testing/e2e-fixtures/stack-tools-11.json";
import toolsPull11Json from "../testing/e2e-fixtures/pull-tools-11.json";
import type { MiddlemanClient } from "../types.js";
import ActivityFeedView from "./ActivityFeedView.svelte";

const toolsPull11 = toolsPull11Json as unknown as PullDetail;

// The seeded server's GET response for tools#10 (no standalone fixture was
// captured): same repo/capabilities envelope as tools#11 with the #10 member
// identity from the captured stack fixtures and its stack at position 1.
function toolsPull10(): PullDetail {
  const detail = structuredClone(toolsPull11);
  detail.merge_request.Number = 10;
  detail.merge_request.Title = "Auth: extract token refresh helper";
  detail.merge_request.URL = "https://github.com/acme/tools/pull/10";
  detail.merge_request.HeadBranch = "feat/auth-base";
  detail.merge_request.BaseBranch = "main";
  detail.stack = structuredClone(stackTools10Json) as PullDetail["stack"];
  return detail;
}

const activityItems = (activityJson as { items: ActivityItem[] }).items;
const tools11Activity = activityItems.find((item) => item.id === "pr:11");
if (!tools11Activity) {
  throw new Error("activity fixture missing the acme/tools#11 new_pr item");
}

// jsdom lacks ResizeObserver (dimension bindings) and the Web Animations API
// (Svelte transitions); both are exercised by the activity shell + timeline.
type GlobalWithResizeObserver = { ResizeObserver?: unknown };
type AnimatableElement = HTMLElement & { animate?: (...args: unknown[]) => unknown };
let originalResizeObserver: unknown;
let resizeObserverExisted = false;
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
  resizeObserverExisted = "ResizeObserver" in globalThis;
  originalResizeObserver = (globalThis as GlobalWithResizeObserver).ResizeObserver;
  class ResizeObserverStub {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  (globalThis as GlobalWithResizeObserver).ResizeObserver = ResizeObserverStub;

  const proto = Element.prototype as unknown as AnimatableElement;
  originalAnimate = proto.animate;
  if (typeof proto.animate !== "function") {
    proto.animate = () => new AnimationStub();
    animatePolyfilled = true;
  }
});

afterAll(() => {
  if (resizeObserverExisted) {
    (globalThis as GlobalWithResizeObserver).ResizeObserver = originalResizeObserver;
  } else {
    delete (globalThis as GlobalWithResizeObserver).ResizeObserver;
  }
  if (animatePolyfilled) {
    const proto = Element.prototype as unknown as AnimatableElement;
    if (originalAnimate) {
      proto.animate = originalAnimate;
    } else {
      delete proto.animate;
    }
  }
});

type PathParams = { number?: number };

function fakeClient(): MiddlemanClient {
  return {
    GET: vi.fn(async (path: string, init?: { params?: { path?: PathParams } }) => {
      const number = init?.params?.path?.number;
      if (path === "/pulls/{provider}/{owner}/{name}/{number}") {
        if (number === 11) return { data: structuredClone(toolsPull11) };
        if (number === 10) return { data: toolsPull10() };
        return { error: { title: "not found", detail: `pull ${number}` } };
      }
      if (path === "/pulls/{provider}/{owner}/{name}/{number}/stack") {
        if (number === 11) return { data: structuredClone(stackTools11Json) };
        if (number === 10) return { data: structuredClone(stackTools10Json) };
        return { error: { title: "not found", detail: `stack ${number}` } };
      }
      if (path === "/repo/{provider}/{owner}/{name}") {
        return {
          data: {
            AllowSquashMerge: false,
            AllowMergeCommit: false,
            AllowRebaseMerge: false,
            ViewerCanMerge: false,
          },
        };
      }
      return { error: { title: "unexpected GET", detail: path } };
    }),
    POST: vi.fn(async () => ({ error: { title: "mutations disabled in this test" } })),
  } as unknown as MiddlemanClient;
}

function activityStoreMock(items: ActivityItem[]) {
  return {
    initializeFromMount: vi.fn(),
    loadActivity: vi.fn(async () => undefined),
    startActivityPolling: vi.fn(),
    stopActivityPolling: vi.fn(),
    getActivitySearch: () => "",
    getEnabledEvents: () => new Set(["new_pr", "comment", "review", "commit", "force_push"]),
    getHideClosedMerged: () => false,
    getHideBots: () => false,
    getHideDefaultBranchActivity: () => false,
    getItemFilter: () => "all",
    getActivityItems: () => items,
    getActivityError: () => null,
    getViewMode: () => "flat" as const,
    getTimeRange: () => "7d",
    isActivityLoading: () => false,
    isActivityCapped: () => false,
    getCollapseThreads: () => false,
    collapseAllThreads: vi.fn(),
    expandAllThreads: vi.fn(),
    isThreadItemExpanded: () => true,
    toggleThreadItem: vi.fn(),
    setActivityFilterTypes: vi.fn(),
    setItemFilter: vi.fn(),
    setEnabledEvents: vi.fn(),
    setHideClosedMerged: vi.fn(),
    setHideBots: vi.fn(),
    setHideDefaultBranchActivity: vi.fn(),
    setActivitySearch: vi.fn(),
    setTimeRange: vi.fn(),
    setViewMode: vi.fn(),
    syncToURL: vi.fn(),
  };
}

function renderActivityFeedView() {
  const navigate = vi.fn();
  const onDrawerItemChange = vi.fn();
  const client = fakeClient();
  const detail = createDetailStore({ client });
  const rendered = render(ActivityFeedView, {
    props: { onDrawerItemChange },
    context: new Map<symbol, unknown>([
      [API_CLIENT_KEY, client],
      [
        STORES_KEY,
        {
          activity: activityStoreMock([structuredClone(tools11Activity!)]),
          detail,
          pulls: { loadPulls: vi.fn() },
          settings: {
            isSettingsLoaded: () => true,
            hasConfiguredRepos: () => true,
          },
          sync: {
            subscribeSyncComplete: vi.fn(() => () => undefined),
            getSyncState: () => null,
          },
          grouping: {
            getGroupByRepo: () => false,
            setGroupByRepo: vi.fn(),
            getHideOrgName: () => false,
            setHideOrgName: vi.fn(),
          },
        },
      ],
      [NAVIGATE_KEY, navigate],
      [ACTIONS_KEY, { pull: [], issue: [] }],
      [UI_CONFIG_KEY, { hideStar: true }],
      [HOST_STATE_KEY, {}],
      [
        SIDEBAR_KEY,
        {
          isEmbedded: () => false,
          isSidebarToggleEnabled: () => false,
          toggleSidebar: vi.fn(),
        },
      ],
    ]),
  });
  return { ...rendered, navigate, onDrawerItemChange };
}

function drawerHeaderText(container: HTMLElement): string {
  return container.querySelector(".activity-detail-header")?.textContent?.trim() ?? "";
}

describe("ActivityFeedView stack member drawer navigation", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("swaps the drawer to the clicked stack member and reports the new drawer item", async () => {
    const { container, navigate, onDrawerItemChange } = renderActivityFeedView();

    // Open the drawer from the seeded tools#11 activity row.
    const row = [...container.querySelectorAll<HTMLElement>(".activity-row")].find((candidate) =>
      candidate.textContent?.includes("Auth: add retry with backoff"),
    );
    expect(row).toBeDefined();
    await fireEvent.click(row!);

    const detailPane = container.querySelector<HTMLElement>(".activity-detail");
    expect(detailPane).not.toBeNull();
    expect(drawerHeaderText(container)).toContain("acme/tools#11");

    // PullDetail loads tools#11 through the real detail store + fake client.
    const chip = await waitFor(() => {
      const found = detailPane!.querySelector<HTMLButtonElement>('[data-testid="stack-chip"]');
      expect(found).not.toBeNull();
      return found!;
    });
    await fireEvent.click(chip);

    const panel = detailPane!.querySelector<HTMLElement>(".stack-panel");
    expect(panel).not.toBeNull();
    expect(panel!.textContent).toContain("3 PRs · current 2/3 · downstack conflict");

    await fireEvent.click(within(detailPane!).getByRole("button", { name: "#10 Auth: extract token refresh helper" }));

    // handleStackMemberNavigate swapped the (uncontrolled) drawer item...
    expect(drawerHeaderText(container)).toContain("acme/tools#10");
    // ...and reported the ref App uses to write ?selected=pr:10&repo_path=...
    expect(onDrawerItemChange).toHaveBeenCalledTimes(1);
    expect(onDrawerItemChange).toHaveBeenCalledWith({
      itemType: "pr",
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "tools",
      repoPath: "acme/tools",
      number: 10,
      detailTab: "conversation",
    });
    // The drawer handled it (returned true), so no route navigation fired.
    expect(navigate).not.toHaveBeenCalled();

    // The stack panel re-renders for #10 as the current member (1/3).
    await waitFor(() => {
      const summary = detailPane!.querySelector(".stack-summary");
      expect(summary?.textContent).toContain("3 PRs · current 1/3");
    });
    await waitFor(() => {
      expect(screen.getByText("This branch has conflicts that must be resolved before merging.")).toBeTruthy();
    });
  });
});
