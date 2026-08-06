import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const mockSetPullRequestSettings = vi.fn();
const mockPersistSettings = vi.hoisted(() => vi.fn());

vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({
    settings: { setPullRequestSettings: mockSetPullRequestSettings },
  }),
}));

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      persist: (request) => mockPersistSettings(request),
    }),
  };
});

vi.mock("../../stores/embed-config.svelte.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../stores/embed-config.svelte.js")>()),
  isEmbedded: () => false,
}));

import PullRequestSettings from "./PullRequestSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

const initial = {
  allow_mid_stack_merges: false,
  prefer_github_native_stacks: false,
};

describe("PullRequestSettings", () => {
  afterEach(() => {
    cleanup();
    mockSetPullRequestSettings.mockReset();
    mockPersistSettings.mockReset();
  });

  it("saves the GitHub native stack preference", async () => {
    const onUpdate = vi.fn();
    const saved = { ...initial, prefer_github_native_stacks: true };
    mockPersistSettings.mockReturnValue(Effect.succeed({ pull_requests: saved }));
    render(SettingsRuntimeHarness, {
      props: { component: PullRequestSettings, componentProps: { pullRequests: initial, onUpdate } },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Prefer GitHub native stacks" }));

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ pull_requests: saved });
    expect(onUpdate).toHaveBeenNthCalledWith(1, saved);
    expect(onUpdate).toHaveBeenLastCalledWith(saved);
    expect(mockSetPullRequestSettings).toHaveBeenCalledWith(saved);
  });

  it("restores the prior settings when saving fails", async () => {
    const onUpdate = vi.fn();
    mockPersistSettings.mockReturnValue(
      Effect.fail({ _tag: "TransientTransportError", operation: "save settings", cause: new Error("save failed") }),
    );
    render(SettingsRuntimeHarness, {
      props: { component: PullRequestSettings, componentProps: { pullRequests: initial, onUpdate } },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Prefer GitHub native stacks" }));

    await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(2));
    expect(onUpdate).toHaveBeenLastCalledWith(initial);
    expect(mockSetPullRequestSettings).not.toHaveBeenCalled();
  });
});
