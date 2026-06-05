import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const mockPost = vi.fn();
const mockRefreshDetailOnly = vi.fn();
const mockLoadPulls = vi.fn();

vi.mock("../../packages/ui/src/context.js", () => ({
  getClient: () => ({
    POST: mockPost,
  }),
  getStores: () => ({
    detail: {
      refreshDetailOnly: mockRefreshDetailOnly,
    },
    pulls: {
      loadPulls: mockLoadPulls,
    },
  }),
}));

import ApproveWorkflowsButton from "../../packages/ui/src/components/detail/ApproveWorkflowsButton.svelte";

describe("ApproveWorkflowsButton", () => {
  beforeEach(() => {
    mockPost.mockReset();
    mockRefreshDetailOnly.mockReset();
    mockLoadPulls.mockReset();
    mockRefreshDetailOnly.mockResolvedValue(undefined);
    mockLoadPulls.mockResolvedValue(undefined);
  });

  afterEach(() => {
    cleanup();
  });

  it("renders a count when more than one workflow needs approval", () => {
    render(ApproveWorkflowsButton, {
      props: {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
        number: 7,
        count: 2,
      },
    });

    expect(screen.getByRole("button", { name: /approve workflows \(2\)/i })).toBeTruthy();
  });

  it("posts to approve-workflows and refreshes detail without sync", async () => {
    mockPost.mockResolvedValue({
      data: { status: "approved_workflows", approved_count: 2 },
    });

    render(ApproveWorkflowsButton, {
      props: {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
        number: 7,
        count: 2,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /approve workflows \(2\)/i }));

    expect(mockPost).toHaveBeenCalledWith("/pulls/{provider}/{owner}/{name}/{number}/approve-workflows", {
      params: {
        path: {
          provider: "github",
          owner: "acme",
          name: "widget",
          number: 7,
        },
      },
    });
    expect(mockRefreshDetailOnly).toHaveBeenCalledWith("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    });
    expect(mockLoadPulls).toHaveBeenCalledTimes(1);
  });

  it("shows an inline error when approval fails", async () => {
    mockPost.mockResolvedValue({
      error: { detail: "GitHub API error" },
    });

    render(ApproveWorkflowsButton, {
      props: {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
        number: 7,
        count: 1,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /^approve workflows$/i }));

    expect(screen.getByText("GitHub API error")).toBeTruthy();
    expect(mockRefreshDetailOnly).not.toHaveBeenCalled();
  });
});
