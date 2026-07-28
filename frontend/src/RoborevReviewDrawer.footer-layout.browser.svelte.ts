// Guards the review drawer footer layout invariant: the action buttons are a
// single horizontal group and never stack, and when the footer runs out of
// room it is the token usage summary that drops to a second row.
//
// The original bug was exactly this failure: a long non-wrapping usage string
// squeezed the actions until they wrapped one-per-line, turning the drawer's
// primary controls into what read as a vertical list of links. Nothing about
// that is observable in jsdom -- it needs real wrapping and real rects -- and
// it needs no backend, so it belongs in the browser lane rather than
// Playwright.

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

function actionRects(): DOMRect[] {
  const group = document.querySelector<HTMLElement>(".footer-actions")!;
  return [...group.querySelectorAll("button")].map((el) => el.getBoundingClientRect());
}

function usageRect(): DOMRect {
  return document.querySelector<HTMLElement>(".token-usage")!.getBoundingClientRect();
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

  it("keeps the actions beside the usage summary when the footer has room", () => {
    mounted = mountAt(1100);

    const actions = assertActionsOnOneRow();
    const usage = usageRect();
    const actionsBottom = Math.max(...actions.map((r) => r.bottom));

    expect(usage.top).toBeLessThan(actionsBottom);
    expect(usage.left).toBeGreaterThanOrEqual(Math.max(...actions.map((r) => r.right)));
  });

  // 260px is below the group's own intrinsic width: wide enough that the
  // buttons still render, narrow enough that a wrappable group would break
  // one-per-line. Verified by re-running this against `flex-wrap: wrap` on
  // .footer-actions, which stacks them and fails the row assertion.
  it("wraps the usage summary below rather than stacking the actions when space is tight", () => {
    mounted = mountAt(260);

    const actions = assertActionsOnOneRow();
    const usage = usageRect();
    const actionsBottom = Math.max(...actions.map((r) => r.bottom));

    expect(usage.top).toBeGreaterThanOrEqual(actionsBottom);
  });
});
