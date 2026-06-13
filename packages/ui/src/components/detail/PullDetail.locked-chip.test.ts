// Vitest component conversion of the PR-detail case from
// frontend/tests/e2e-full/provider-capabilities.spec.ts: a locked GitHub pull
// request shows the "Locked" chip in the chips row because GitHub is a
// provider that supports locking. Uses the acme/widgets#1 detail fetched live
// from the seeded e2e Go server (IsLocked: true).
import { cleanup, render, within } from "@testing-library/svelte";
import { afterEach, beforeAll, describe, expect, it, vi } from "vite-plus/test";
import type { PullDetail } from "../../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, STORES_KEY, UI_CONFIG_KEY } from "../../context.js";
import { seedGet } from "../../testing/seedClient.js";
import PullDetailComponent from "./PullDetail.svelte";

let widgetsPull1: PullDetail;

beforeAll(async () => {
  widgetsPull1 = await seedGet<PullDetail>("/api/v1/pulls/github/acme/widgets/1");
});

function renderPullDetail(detail: PullDetail) {
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
  return render(PullDetailComponent, {
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
}

describe("PullDetail locked chip (provider capabilities)", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows the Locked chip in the chips row for a locked PR on a provider that supports locking", () => {
    const detail = structuredClone(widgetsPull1);
    // Guard the fixture precondition the assertion depends on: the seeded
    // server marked widgets#1 as locked on github (a locked-capable provider).
    expect(detail.merge_request.IsLocked).toBe(true);
    expect(detail.repo.provider).toBe("github");

    renderPullDetail(detail);

    const chipsRow = document.querySelector<HTMLElement>(".chips-row");
    expect(chipsRow).not.toBeNull();
    const locked = within(chipsRow!).getByText("Locked", { exact: true });
    expect(locked).toBeTruthy();
    expect(locked.closest("[title]")?.getAttribute("title")).toBe("This pull request is locked");
  });

  it("hides the Locked chip when the PR is not locked", () => {
    const detail = structuredClone(widgetsPull1);
    detail.merge_request.IsLocked = false;

    renderPullDetail(detail);

    const chipsRow = document.querySelector<HTMLElement>(".chips-row");
    expect(chipsRow).not.toBeNull();
    expect(within(chipsRow!).queryByText("Locked", { exact: true })).toBeNull();
  });
});
