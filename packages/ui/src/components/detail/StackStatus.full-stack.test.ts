// Vitest component conversion of the first two cases of
// frontend/tests/e2e-full/stack-status.spec.ts, driving the acme/tools auth
// stack data fetched live from the seeded e2e Go server (see seedClient):
// - GET /pulls/github/acme/tools/11: MergeRequestDetailResponse whose `stack`
//   field holds the 3-member auth stack (#10/#11/#12, all mergeable "dirty").
// - GET /pulls/github/acme/tools/{11,10}/stack: the full-stack responses.
//
// Case 2 ("stack member navigation preserves the focus route") is converted
// as a 3-part contract mapping (browser URL assertions cannot run in jsdom):
// (a) StackStatus.navigateToMember invokes onmembernavigate with the full
//     member ref and skips the default navigate when the callback returns
//     true; without a callback it navigates to buildPullRequestRoute(ref).
// (b) buildFocusPullRequestRoute — the builder App.svelte's
//     handleResponsiveStackMemberNavigate feeds to navigate() under focus
//     presentation — yields /focus/pulls/github/acme/tools/10 for that ref.
// (c) rendering StackStatus for #10 from its /stack response shows the
//     "current 1/3" summary the e2e asserted after navigation.
import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterEach, beforeAll, describe, expect, it, vi } from "vite-plus/test";
import type { PullDetail } from "../../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, STORES_KEY, UI_CONFIG_KEY } from "../../context.js";
import { buildFocusPullRequestRoute, buildPullRequestRoute } from "../../routes.js";
import { seedGet } from "../../testing/seedClient.js";
import PullDetailComponent from "./PullDetail.svelte";
import StackStatus from "./StackStatus.svelte";

let toolsPull11: PullDetail;
let stackTools11: unknown;
let stackTools10: unknown;

beforeAll(async () => {
  toolsPull11 = await seedGet<PullDetail>("/api/v1/pulls/github/acme/tools/11");
  stackTools11 = await seedGet("/api/v1/pulls/github/acme/tools/11/stack");
  stackTools10 = await seedGet("/api/v1/pulls/github/acme/tools/10/stack");
});

const toolsRef = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "tools",
  repoPath: "acme/tools",
};

function renderPullDetail(detail: PullDetail) {
  const navigate = vi.fn();
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
      [NAVIGATE_KEY, navigate],
    ]),
  });
  return { ...rendered, navigate };
}

function renderStackStatus(
  number: number,
  initialStack: unknown,
  options: { onmembernavigate?: (ref: unknown) => boolean | void } = {},
) {
  const navigate = vi.fn();
  const rendered = render(StackStatus, {
    props: {
      ...toolsRef,
      number,
      initialStack,
      expanded: true,
      ...(options.onmembernavigate ? { onmembernavigate: options.onmembernavigate } : {}),
    },
    context: new Map<symbol, unknown>([
      [API_CLIENT_KEY, { GET: vi.fn(async () => ({ data: null })) }],
      [STORES_KEY, {}],
      [NAVIGATE_KEY, navigate],
    ]),
  });
  return { ...rendered, navigate };
}

describe("StackStatus full-stack rendering", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders a passive base row from the full-stack data", async () => {
    const { navigate } = renderPullDetail(structuredClone(toolsPull11));

    expect(screen.getByText("This branch has conflicts that must be resolved before merging.")).toBeTruthy();

    const chip = document.querySelector<HTMLButtonElement>('[data-testid="stack-chip"]');
    expect(chip).not.toBeNull();
    await fireEvent.click(chip!);

    const panel = document.querySelector<HTMLElement>(".stack-panel");
    expect(panel).not.toBeNull();
    expect(panel!.textContent).toContain("3 PRs · current 2/3 · downstack conflict");
    expect(within(panel!).getAllByText("× Conflicts")).toHaveLength(3);

    const memberLinks = [...panel!.querySelectorAll<HTMLButtonElement>(".stack-member-link")].map((link) =>
      link.textContent?.replace(/\s+/g, " ").trim(),
    );
    expect(memberLinks).toEqual([
      "#12 Auth: error handling UI",
      "#11 Auth: add retry with backoff",
      "#10 Auth: extract token refresh helper",
    ]);

    const baseRow = panel!.querySelector<HTMLElement>(".stack-row--base");
    expect(baseRow).not.toBeNull();
    expect(baseRow!.getAttribute("aria-label")).toBe("Stack base main");
    expect(baseRow!.querySelector(".stack-base-name")?.textContent?.trim()).toBe("main");
    expect(baseRow!.querySelectorAll(".stack-member-link")).toHaveLength(0);

    // Expanding the chip is a passive toggle: it never routes anywhere.
    expect(navigate).not.toHaveBeenCalled();
  });

  it("hands member navigation to onmembernavigate and skips default routing when handled", async () => {
    const onmembernavigate = vi.fn(() => true);
    const { navigate } = renderStackStatus(11, structuredClone(stackTools11), { onmembernavigate });

    await fireEvent.click(screen.getByRole("button", { name: "#10 Auth: extract token refresh helper" }));

    expect(onmembernavigate).toHaveBeenCalledTimes(1);
    expect(onmembernavigate).toHaveBeenCalledWith({
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "tools",
      repoPath: "acme/tools",
      number: 10,
    });
    // Callback returned true -> the default navigate must not also fire.
    expect(navigate).not.toHaveBeenCalled();
  });

  it("falls back to the canonical pull route when no member-navigate handler is given", async () => {
    const { navigate } = renderStackStatus(11, structuredClone(stackTools11));

    await fireEvent.click(screen.getByRole("button", { name: "#10 Auth: extract token refresh helper" }));

    expect(navigate).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith("/pulls/github/acme/tools/10");
    // The literal above is the independently-known route; the shared builder
    // the rest of the app uses must agree with it.
    expect(buildPullRequestRoute({ ...toolsRef, number: 10 })).toBe("/pulls/github/acme/tools/10");
  });

  it("maps the member ref to the focus route App uses for focus presentation", () => {
    // App.svelte handleResponsiveStackMemberNavigate navigates to
    // buildFocusPullRequestRoute(ref) when a stack member is clicked under
    // focus presentation; the e2e asserted the resulting browser URL.
    expect(buildFocusPullRequestRoute({ ...toolsRef, number: 10 })).toBe("/focus/pulls/github/acme/tools/10");
  });

  it("shows the downstack member as current 1/3 after navigating to #10", () => {
    renderStackStatus(10, structuredClone(stackTools10));

    const panel = document.querySelector<HTMLElement>(".stack-panel");
    expect(panel).not.toBeNull();
    expect(panel!.querySelector(".stack-summary")?.textContent).toContain("3 PRs · current 1/3");
  });
});
