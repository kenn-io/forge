import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { replaceUrl } from "../../stores/router.svelte.js";

const mocks = vi.hoisted(() => ({
  getSettings: vi.fn(),
  showFlash: vi.fn(),
}));

vi.mock("../../api/settings.js", () => ({
  getSettings: mocks.getSettings,
}));

vi.mock("@middleman/ui/stores/flash", () => ({
  showFlash: mocks.showFlash,
}));

import WorkspaceEmbedShell from "./WorkspaceEmbedShell.svelte";

describe("WorkspaceEmbedShell settings requests", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.getSettings.mockReset();
    mocks.showFlash.mockReset();
    mocks.getSettings.mockImplementation(
      (options?: { signal?: AbortSignal }) =>
        new Promise((_resolve, reject) => {
          options?.signal?.addEventListener("abort", () => {
            reject(options.signal?.reason);
          });
        }),
    );
    replaceUrl("/workspaces/embed/terminal/ws-1");
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    replaceUrl("/");
  });

  it("aborts settings requests on timeout, retry, and unmount", async () => {
    const mounted = render(WorkspaceEmbedShell);
    await waitFor(() => expect(mocks.getSettings).toHaveBeenCalledTimes(1));
    const firstSignal = mocks.getSettings.mock.calls[0]?.[0]?.signal as AbortSignal | undefined;

    await vi.advanceTimersByTimeAsync(8_000);
    await waitFor(() => expect(screen.getByRole("button", { name: "Retry terminal settings" })).toBeTruthy());
    expect(firstSignal?.aborted).toBe(true);

    await fireEvent.click(screen.getByRole("button", { name: "Retry terminal settings" }));
    await waitFor(() => expect(mocks.getSettings).toHaveBeenCalledTimes(2));
    const secondSignal = mocks.getSettings.mock.calls[1]?.[0]?.signal as AbortSignal | undefined;
    expect(firstSignal?.aborted).toBe(true);
    expect(secondSignal?.aborted).toBe(false);

    mounted.unmount();
    expect(secondSignal?.aborted).toBe(true);
  });
});
