import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const { mockPersistSettings, workspaceStore } = vi.hoisted(() => {
  let current = { auto_assign_on_create: false, default_sidebar_view: "diff" as "diff" | "item" };
  return {
    mockPersistSettings: vi.fn(),
    workspaceStore: {
      getWorkspaceSettings: () => current,
      setWorkspaceSettings: (settings: typeof current) => {
        current = settings;
      },
      reset: () => {
        current = { auto_assign_on_create: false, default_sidebar_view: "diff" };
      },
    },
  };
});

vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({ settings: workspaceStore }),
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

import WorkspaceSettings from "./WorkspaceSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

const initial = { auto_assign_on_create: false, default_sidebar_view: "diff" as const };

describe("WorkspaceSettings", () => {
  afterEach(() => {
    cleanup();
    mockPersistSettings.mockReset();
    workspaceStore.reset();
  });

  it("saves automatic assignment for new workspace items", async () => {
    const onUpdate = vi.fn();
    const saved = { ...initial, auto_assign_on_create: true };
    mockPersistSettings.mockReturnValue(Effect.succeed({ workspaces: saved }));
    render(SettingsRuntimeHarness, {
      props: { component: WorkspaceSettings, componentProps: { workspaces: initial, onUpdate } },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Assign new workspace items to me" }));

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ workspaces: saved });
    expect(onUpdate).toHaveBeenNthCalledWith(1, saved);
    expect(onUpdate).toHaveBeenLastCalledWith(saved);
  });

  it("saves the default sidebar view without changing automatic assignment", async () => {
    const onUpdate = vi.fn();
    const saved = { ...initial, default_sidebar_view: "item" as const };
    mockPersistSettings.mockReturnValue(Effect.succeed({ workspaces: saved }));
    render(SettingsRuntimeHarness, {
      props: { component: WorkspaceSettings, componentProps: { workspaces: initial, onUpdate } },
    });

    await fireEvent.click(screen.getByRole("combobox", { name: "Default sidebar view: Diff" }));
    await fireEvent.click(screen.getByRole("option", { name: "PR/Issue" }));

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ workspaces: saved });
    expect(onUpdate).toHaveBeenNthCalledWith(1, saved);
    expect(onUpdate).toHaveBeenLastCalledWith(saved);
  });

  it("restores the prior setting when saving fails", async () => {
    const onUpdate = vi.fn((workspaces: typeof initial) => workspaceStore.setWorkspaceSettings(workspaces));
    mockPersistSettings.mockReturnValue(
      Effect.fail({ _tag: "TransientTransportError", operation: "save settings", cause: new Error("save failed") }),
    );
    render(SettingsRuntimeHarness, {
      props: { component: WorkspaceSettings, componentProps: { workspaces: initial, onUpdate } },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Assign new workspace items to me" }));

    await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(2));
    expect(onUpdate).toHaveBeenLastCalledWith(initial);
  });
});
