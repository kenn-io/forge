// Guards the review drawer footer layout invariant: the action buttons are a
// single horizontal group and never stack, when the footer runs out of room it
// is the token usage summary that drops to a second row, and once even that is
// not enough the group downgrades to icon-only controls that stay inside the
// footer.
//
// The original bug was exactly this failure: a long non-wrapping usage string
// squeezed the actions until they wrapped one-per-line, turning the drawer's
// primary controls into what read as a vertical list of links. The narrow case
// is the same bug's other half -- a non-shrinking row wider than the drawer is
// clipped by the workspace sidebar's overflow, putting actions out of reach.
// Nothing about either is observable in jsdom -- both need real wrapping and
// real rects -- and neither needs a backend, so this belongs in the browser
// lane rather than Playwright.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";

// Layout assertions are only meaningful under the production reset and tokens.
import "./app.css";
import { STORES_KEY } from "../../packages/ui/src/context.js";
import ReviewDrawer from "../../packages/ui/src/components/roborev/ReviewDrawer.svelte";

// A realistic worst case: the blob that originally overflowed the footer.
const job = {
  id: 8549,
  agent: "codex",
  agentic: false,
  enqueued_at: "2026-07-15T00:00:00Z",
  finished_at: "2026-07-15T00:01:00Z",
  git_ref: "f64141d9aa",
  branch: "t3code/session-start-workspace-context",
  job_type: "review",
  prompt_prebuilt: false,
  repo_id: 1,
  repo_name: "example/repo",
  retry_count: 0,
  started_at: "2026-07-15T00:00:10Z",
  status: "done",
  token_usage: JSON.stringify({
    input_tokens: 231582,
    cached_input_tokens: 189952,
    total_output_tokens: 2542,
    peak_context_tokens: 47248,
    cost_usd: 0.347212,
    has_cost: true,
  }),
};

function mountAt(widthPx: number): { unmount: () => void } {
  const wrapper = document.createElement("div");
  wrapper.style.width = `${widthPx}px`;
  document.body.appendChild(wrapper);

  const { unmount } = render(ReviewDrawer, {
    target: wrapper,
    context: new Map<symbol, unknown>([
      [
        STORES_KEY,
        {
          roborevJobs: {
            getVisibleJobs: () => [job],
            getSelectedJobId: () => job.id,
            deselectJob: vi.fn(),
            rerunJob: vi.fn(),
            cancelJob: vi.fn(),
            getPanelMemberError: () => undefined,
            isLoadingMembers: () => false,
            getPanelMembers: () => undefined,
            setPanelMemberInterest: vi.fn(),
            refreshPanelMembers: vi.fn(),
          },
          // The real tab bodies render against this too; the drawer footer is
          // measured with its actual siblings in place rather than stubs.
          roborevReview: {
            getSelectedJob: () => null,
            getSelectedJobId: () => job.id,
            getOutput: () => "No issues found.",
            getPrompt: () => "",
            getResponses: () => [],
            getReview: () => ({ id: 1, job_id: job.id, output: "", closed: false }),
            isLoading: () => false,
            isReviewNotFound: () => false,
            isClosed: () => false,
            addComment: vi.fn(),
            closeReview: vi.fn(),
          },
        },
      ],
    ]),
  });

  return {
    unmount: () => {
      unmount();
      wrapper.remove();
    },
  };
}

// FitStages renders the active stage ahead of its hidden measurement probes,
// so the first group in document order is the one the user sees.
function actionButtons(): HTMLElement[] {
  const group = document.querySelector<HTMLElement>(".footer-actions")!;
  return [...group.querySelectorAll<HTMLElement>("button")];
}

function actionRects(): DOMRect[] {
  return actionButtons().map((el) => el.getBoundingClientRect());
}

function actionNames(): (string | null)[] {
  return actionButtons().map((el) => el.getAttribute("aria-label") ?? el.textContent!.trim());
}

function usageRect(): DOMRect {
  return document.querySelector<HTMLElement>(".token-usage")!.getBoundingClientRect();
}

function footerRect(): DOMRect {
  return document.querySelector<HTMLElement>(".kit-bottom-dock__footer")!.getBoundingClientRect();
}

describe("review drawer footer layout", () => {
  let mounted: { unmount: () => void } | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
    cleanup();
  });

  function assertActionsOnOneRow(): DOMRect[] {
    const rects = actionRects();
    expect(rects.length).toBeGreaterThan(1);
    for (const rect of rects) {
      expect(rect.width).toBeGreaterThan(0);
      expect(Math.abs(rect.top - rects[0]!.top)).toBeLessThanOrEqual(0.5);
    }
    return rects;
  }

  // Every action has to be inside the footer's own box: the drawer sits in a
  // pane that clips its overflow, so anything past the edge is unreachable
  // rather than merely ugly.
  function assertActionsInsideFooter(actions: DOMRect[]): void {
    const footer = footerRect();
    expect(actions[0]!.left).toBeGreaterThanOrEqual(footer.left - 0.5);
    expect(Math.max(...actions.map((r) => r.right))).toBeLessThanOrEqual(footer.right + 0.5);
  }

  it("keeps the actions beside the usage summary when the footer has room", async () => {
    mounted = mountAt(1100);

    await vi.waitFor(() => expect(actionNames()).toEqual(["Close Review", "Rerun", "Copy Output"]));
    expect(actionButtons().every((el) => el.classList.contains("kit-button"))).toBe(true);

    const actions = assertActionsOnOneRow();
    assertActionsInsideFooter(actions);
    const usage = usageRect();
    const actionsBottom = Math.max(...actions.map((r) => r.bottom));

    expect(usage.top).toBeLessThan(actionsBottom);
    expect(usage.left).toBeGreaterThanOrEqual(Math.max(...actions.map((r) => r.right)));
  });

  // 260px is below the labelled group's own intrinsic width: wide enough that
  // the buttons still render, narrow enough that a wrappable group would break
  // one-per-line. Verified by re-running this against `flex-wrap: wrap` on
  // .footer-actions, which stacks them and fails the row assertion.
  it("wraps the usage summary below rather than stacking the actions when space is tight", async () => {
    mounted = mountAt(260);

    await vi.waitFor(() => expect(actionButtons().every((el) => el.classList.contains("kit-icon-button"))).toBe(true));

    const actions = assertActionsOnOneRow();
    assertActionsInsideFooter(actions);
    const usage = usageRect();
    const actionsBottom = Math.max(...actions.map((r) => r.bottom));

    expect(usage.top).toBeGreaterThanOrEqual(actionsBottom);
  });

  // The band between the two cases above is what the fit host's min-width
  // floor protects. Here the usage summary is still narrow enough to claim the
  // first row, leaving the actions less room than even the icon stage needs --
  // without a floor they render over the usage text instead of the summary
  // moving out of the way. Verified by re-running this with the host's
  // min-width removed, which overlaps them and fails.
  it("moves the usage summary out of the way instead of letting the actions render over it", async () => {
    mounted = mountAt(380);

    await vi.waitFor(() => expect(actionButtons()).toHaveLength(3));

    const actions = assertActionsOnOneRow();
    assertActionsInsideFooter(actions);
    const usage = usageRect();

    expect(usage.top).toBeGreaterThanOrEqual(Math.max(...actions.map((r) => r.bottom)));
  });

  // The compact stage is a rendering of the same actions, not a reduced set:
  // the accessible names must not change with the drawer's width, or a
  // screen-reader user loses the action the sighted user still has.
  it("keeps every action reachable under the same name once it downgrades to icons", async () => {
    mounted = mountAt(260);

    await vi.waitFor(() => expect(actionButtons().every((el) => el.classList.contains("kit-icon-button"))).toBe(true));
    expect(actionNames()).toEqual(["Close Review", "Rerun", "Copy Output"]);
  });
});
