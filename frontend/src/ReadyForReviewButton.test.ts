import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const mockPost = vi.fn();
const mockLoadDetail = vi.fn();
const mockLoadPulls = vi.fn();

vi.mock("./lib/context.js", () => ({
  getClient: () => ({
    POST: mockPost,
  }),
  getStores: () => ({
    detail: {
      loadDetail: mockLoadDetail,
    },
    pulls: {
      loadPulls: mockLoadPulls,
    },
  }),
}));

import ReadyForReviewButton from "./lib/components/detail/ReadyForReviewButton.svelte";

describe("ReadyForReviewButton", () => {
  beforeEach(() => {
    mockPost.mockReset();
    mockLoadDetail.mockReset();
    mockLoadPulls.mockReset();
    mockLoadDetail.mockResolvedValue(undefined);
    mockLoadPulls.mockResolvedValue(undefined);
  });

  afterEach(() => {
    cleanup();
  });

  it("refreshes detail and pull lists after marking ready for review", async () => {
    mockPost.mockResolvedValue({
      data: { status: "ready_for_review" },
    });

    render(ReadyForReviewButton, {
      props: {
        provider: "github",
        platformHost: "github.com",
        owner: "wesm",
        name: "kenn-forge",
        repoPath: "wesm/kenn-forge",
        number: 141,
        size: "sm",
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /ready for review/i }));

    expect(mockLoadDetail).toHaveBeenCalledWith("wesm", "kenn-forge", 141, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "wesm/kenn-forge",
    });
    expect(mockLoadPulls).toHaveBeenCalledTimes(1);
  });

  it("refreshes stale draft state after a GitHub 404", async () => {
    mockPost.mockResolvedValue({
      error: {
        detail:
          "marking wesm/kenn-forge#141 ready for review: POST https://api.github.com/repos/wesm/kenn-forge/pulls/141/ready_for_review: 404 Not Found []",
      },
    });

    render(ReadyForReviewButton, {
      props: {
        provider: "github",
        platformHost: "github.com",
        owner: "wesm",
        name: "kenn-forge",
        repoPath: "wesm/kenn-forge",
        number: 141,
        size: "sm",
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /ready for review/i }));

    expect(mockLoadDetail).toHaveBeenCalledWith("wesm", "kenn-forge", 141, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "wesm/kenn-forge",
    });
    expect(mockLoadPulls).toHaveBeenCalledTimes(1);
  });
});
