import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const mockMarkPullReady = vi.fn();

vi.mock("./lib/context.js", () => ({
  getStores: () => ({
    detail: {
      markPullReady: mockMarkPullReady,
    },
  }),
}));

import ReadyForReviewButton from "./lib/components/detail/ReadyForReviewButton.svelte";

describe("ReadyForReviewButton", () => {
  beforeEach(() => {
    mockMarkPullReady.mockReset();
  });

  afterEach(() => {
    cleanup();
  });

  it("stays pending until the acknowledged ready command settles", async () => {
    let settle = () => {};
    mockMarkPullReady.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as { onSuccess?: () => void; onSettled?: () => void };
      settle = () => {
        callbacks.onSuccess?.();
        callbacks.onSettled?.();
      };
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

    expect(mockMarkPullReady).toHaveBeenCalledWith(
      {
        provider: "github",
        platformHost: "github.com",
        owner: "wesm",
        name: "kenn-forge",
        repoPath: "wesm/kenn-forge",
      },
      141,
      expect.any(Object),
    );
    expect(screen.getByRole("button", { name: "Publishing…" })).toBeTruthy();
    settle();
    await waitFor(() => expect(screen.getByRole("button", { name: /ready for review/i })).toBeTruthy());
  });

  it("settles after an acknowledged failure", async () => {
    mockMarkPullReady.mockImplementation((...args: unknown[]) => {
      const callbacks = args.at(-1) as { onFailure?: (message: string) => void; onSettled?: () => void };
      callbacks.onFailure?.("permission denied");
      callbacks.onSettled?.();
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

    expect(mockMarkPullReady).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: /ready for review/i })).toBeTruthy();
  });
});
