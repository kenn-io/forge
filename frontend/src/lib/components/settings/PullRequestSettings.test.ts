import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const mockSetPullRequestSettings = vi.fn();

vi.mock("@kenn-forge/ui", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@kenn-forge/ui")>()),
  getStores: () => ({
    settings: { setPullRequestSettings: mockSetPullRequestSettings },
  }),
}));

vi.mock("../../api/settings.js", () => ({
  updateSettings: vi.fn(),
}));

vi.mock("../../stores/embed-config.svelte.js", () => ({
  isEmbedded: () => false,
}));

import { updateSettings } from "../../api/settings.js";
import PullRequestSettings from "./PullRequestSettings.svelte";

const mockUpdateSettings = vi.mocked(updateSettings);
const initial = {
  allow_mid_stack_merges: false,
  prefer_github_native_stacks: false,
};

describe("PullRequestSettings", () => {
  afterEach(() => {
    cleanup();
    mockSetPullRequestSettings.mockReset();
    mockUpdateSettings.mockReset();
  });

  it("saves the GitHub native stack preference", async () => {
    const onUpdate = vi.fn();
    const saved = { ...initial, prefer_github_native_stacks: true };
    mockUpdateSettings.mockResolvedValue({ pull_requests: saved } as never);
    render(PullRequestSettings, {
      props: { pullRequests: initial, onUpdate },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Prefer GitHub native stacks" }));

    await waitFor(() => expect(mockUpdateSettings).toHaveBeenCalledWith({ pull_requests: saved }));
    expect(onUpdate).toHaveBeenNthCalledWith(1, saved);
    expect(onUpdate).toHaveBeenLastCalledWith(saved);
    expect(mockSetPullRequestSettings).toHaveBeenCalledWith(saved);
  });

  it("restores the prior settings when saving fails", async () => {
    const onUpdate = vi.fn();
    mockUpdateSettings.mockRejectedValue(new Error("save failed"));
    render(PullRequestSettings, {
      props: { pullRequests: initial, onUpdate },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Prefer GitHub native stacks" }));

    await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(2));
    expect(onUpdate).toHaveBeenLastCalledWith(initial);
    expect(mockSetPullRequestSettings).not.toHaveBeenCalled();
  });
});
