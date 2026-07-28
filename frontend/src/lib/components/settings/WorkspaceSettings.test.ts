import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

vi.mock("../../api/settings.js", () => ({
  updateSettings: vi.fn(),
}));

vi.mock("../../stores/embed-config.svelte.js", () => ({
  isEmbedded: () => false,
}));

import { updateSettings } from "../../api/settings.js";
import WorkspaceSettings from "./WorkspaceSettings.svelte";

const mockUpdateSettings = vi.mocked(updateSettings);
const initial = { auto_assign_on_create: false };

describe("WorkspaceSettings", () => {
  afterEach(() => {
    cleanup();
    mockUpdateSettings.mockReset();
  });

  it("saves automatic assignment for new workspace items", async () => {
    const onUpdate = vi.fn();
    const saved = { auto_assign_on_create: true };
    mockUpdateSettings.mockResolvedValue({ workspaces: saved } as never);
    render(WorkspaceSettings, { props: { workspaces: initial, onUpdate } });

    await fireEvent.click(screen.getByRole("button", { name: "Assign new workspace items to me" }));

    await waitFor(() => expect(mockUpdateSettings).toHaveBeenCalledWith({ workspaces: saved }));
    expect(onUpdate).toHaveBeenNthCalledWith(1, saved);
    expect(onUpdate).toHaveBeenLastCalledWith(saved);
  });

  it("restores the prior setting when saving fails", async () => {
    const onUpdate = vi.fn();
    mockUpdateSettings.mockRejectedValue(new Error("save failed"));
    render(WorkspaceSettings, { props: { workspaces: initial, onUpdate } });

    await fireEvent.click(screen.getByRole("button", { name: "Assign new workspace items to me" }));

    await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(2));
    expect(onUpdate).toHaveBeenLastCalledWith(initial);
  });
});
