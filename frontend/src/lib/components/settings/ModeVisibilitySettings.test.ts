import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { ModeVisibility } from "@middleman/ui/api/types";

const { mockSetModeVisibility, mockUpdateSettings } = vi.hoisted(() => ({
  mockSetModeVisibility: vi.fn(),
  mockUpdateSettings: vi.fn(),
}));

vi.mock("@middleman/ui", () => ({
  DEFAULT_MODE_VISIBILITY: {
    activity: true,
    repos: true,
    kata: false,
    docs: false,
    messages: false,
    pulls: true,
    issues: true,
    board: true,
    reviews: true,
    workspaces: true,
  },
  getStores: () => ({
    settings: {
      setModeVisibility: mockSetModeVisibility,
    },
  }),
}));

vi.mock("../../api/settings.js", () => ({
  updateSettings: mockUpdateSettings,
}));

vi.mock("../../stores/embed-config.svelte.js", () => ({
  isEmbedded: () => false,
}));

import ModeVisibilitySettings from "./ModeVisibilitySettings.svelte";

function defaultModes(): ModeVisibility {
  return {
    activity: true,
    repos: true,
    kata: false,
    docs: false,
    messages: false,
    pulls: true,
    issues: true,
    board: true,
    reviews: true,
    workspaces: true,
  };
}

describe("ModeVisibilitySettings", () => {
  afterEach(() => {
    cleanup();
    mockSetModeVisibility.mockReset();
    mockUpdateSettings.mockReset();
  });

  it("persists visible mode changes", async () => {
    const modes = defaultModes();
    const updated = {
      ...modes,
      kata: true,
      docs: true,
      messages: true,
      workspaces: false,
    };
    mockUpdateSettings.mockResolvedValue({ modes: updated });
    const onUpdate = vi.fn();

    render(ModeVisibilitySettings, {
      props: {
        modes,
        onUpdate,
      },
    });

    expect((screen.getByLabelText("Kata") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByLabelText("Docs") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByLabelText("Messages") as HTMLInputElement).checked).toBe(false);

    await fireEvent.click(screen.getByLabelText("Kata"));
    await fireEvent.click(screen.getByLabelText("Docs"));
    await fireEvent.click(screen.getByLabelText("Messages"));
    await fireEvent.click(screen.getByLabelText("Workspaces"));
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({ modes: updated });
    });
    expect(mockSetModeVisibility).toHaveBeenCalledWith(updated);
    expect(onUpdate).toHaveBeenCalledWith(updated);
  });
});
