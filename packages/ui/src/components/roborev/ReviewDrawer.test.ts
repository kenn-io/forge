import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { components } from "../../api/roborev/generated/schema.js";

vi.mock("./ReviewContent.svelte", async () => ({
  default: (await import("./ReviewDrawerTestContent.svelte")).default,
}));
vi.mock("./ResponseList.svelte", async () => ({
  default: (await import("./ReviewDrawerTestContent.svelte")).default,
}));
vi.mock("./LogViewer.svelte", async () => ({
  default: (await import("./ReviewDrawerTestContent.svelte")).default,
}));
vi.mock("./PromptViewer.svelte", async () => ({
  default: (await import("./ReviewDrawerTestContent.svelte")).default,
}));

const state = vi.hoisted(() => ({
  selectedJobId: 42 as number | undefined,
  deselectJob: vi.fn(),
  rerunJob: vi.fn(),
  cancelJob: vi.fn(),
  closeReview: vi.fn(),
}));

type ReviewJob = components["schemas"]["ReviewJob"];
const job: ReviewJob = {
  id: 42,
  agent: "claude",
  agentic: false,
  enqueued_at: "2026-07-15T00:00:00Z",
  finished_at: "2026-07-15T00:01:00Z",
  git_ref: "abcdef123456",
  job_type: "review",
  prompt_prebuilt: false,
  repo_id: 1,
  repo_name: "example/repo",
  retry_count: 0,
  started_at: "2026-07-15T00:00:10Z",
  status: "done",
  token_usage: "12k tokens",
};

vi.mock("../../context.js", () => ({
  getStores: () => ({
    roborevJobs: {
      getVisibleJobs: () => [job],
      getSelectedJobId: () => state.selectedJobId,
      deselectJob: state.deselectJob,
      rerunJob: state.rerunJob,
      cancelJob: state.cancelJob,
      getPanelMemberError: () => undefined,
      isLoadingMembers: () => false,
      getPanelMembers: () => undefined,
      setPanelMemberInterest: vi.fn(),
      refreshPanelMembers: vi.fn(),
    },
    roborevReview: {
      getSelectedJob: () => null,
      getOutput: () => "review output",
      getReview: () => ({ id: 1, job_id: 42, output: "review output", closed: false }),
      isClosed: () => false,
      closeReview: state.closeReview,
    },
  }),
}));

import ReviewDrawer from "./ReviewDrawer.svelte";

describe("ReviewDrawer", () => {
  beforeEach(() => {
    state.selectedJobId = 42;
    state.deselectJob.mockReset();
    state.rerunJob.mockReset();
    state.cancelJob.mockReset();
    state.closeReview.mockReset();
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe(): void {}
        unobserve(): void {}
        disconnect(): void {}
      },
    );
    vi.stubGlobal(
      "MutationObserver",
      class {
        observe(): void {}
        disconnect(): void {}
      },
    );
    vi.stubGlobal("requestAnimationFrame", () => 1);
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      width: 900,
      height: 400,
      x: 0,
      y: 0,
      top: 0,
      right: 900,
      bottom: 400,
      left: 0,
      toJSON: () => ({}),
    });
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("renders the shared bottom dock with stable header, footer, and close control", async () => {
    render(ReviewDrawer);

    const dock = screen.getByRole("region", { name: "Review details" });
    expect(dock.classList.contains("kit-bottom-dock")).toBe(true);
    expect(dock.querySelector(".review-dock-header")).toBeTruthy();
    expect(dock.querySelector(".review-dock-footer")).toBeTruthy();
    expect(screen.getByText("12k tokens")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Close review details" }));
    expect(state.deselectJob).toHaveBeenCalledTimes(1);
  });

  it("keeps review tabs and actions application-owned", async () => {
    render(ReviewDrawer);

    await fireEvent.click(screen.getByRole("button", { name: "Log" }));
    expect(screen.getByTestId("review-drawer-content")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Rerun" }));
    expect(state.rerunJob).toHaveBeenCalledWith(42);

    await fireEvent.click(screen.getByRole("button", { name: "Close Review" }));
    expect(state.closeReview).toHaveBeenCalledWith(42);
  });
});
