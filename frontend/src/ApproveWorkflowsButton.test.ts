import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import * as flash from "./lib/stores/flash.svelte.js";

const mockApprovePullWorkflows = vi.fn();

vi.mock("./lib/context.js", () => ({
  getStores: () => ({
    detail: {
      approvePullWorkflows: mockApprovePullWorkflows,
    },
  }),
}));

import ApproveWorkflowsButton from "./lib/components/detail/ApproveWorkflowsButton.svelte";

describe("ApproveWorkflowsButton", () => {
  beforeEach(() => {
    mockApprovePullWorkflows.mockReset();
  });

  afterEach(() => {
    cleanup();
    for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
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

  it("launches workflow approval and stays pending until acknowledgement", async () => {
    let settle = () => {};
    mockApprovePullWorkflows.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as { onSuccess?: () => void; onSettled?: () => void };
      settle = () => {
        callbacks.onSuccess?.();
        callbacks.onSettled?.();
      };
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

    expect(mockApprovePullWorkflows).toHaveBeenCalledWith(
      {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
      },
      7,
      expect.any(Object),
    );
    expect(screen.getByRole("button", { name: /approving workflows/i })).toBeTruthy();
    settle();
    await waitFor(() => expect(screen.getByRole("button", { name: /approve workflows \(2\)/i })).toBeTruthy());
  });

  it("shows a danger flash when approval fails", async () => {
    mockApprovePullWorkflows.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as { onFailure?: (message: string) => void; onSettled?: () => void };
      callbacks.onFailure?.("GitHub API error");
      callbacks.onSettled?.();
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

    expect(flash.getFlash()).toMatchObject({
      message: "GitHub API error",
      tone: "danger",
    });
    expect(screen.queryByText("GitHub API error")).toBeNull();
    expect(mockApprovePullWorkflows).toHaveBeenCalledOnce();
  });
});
