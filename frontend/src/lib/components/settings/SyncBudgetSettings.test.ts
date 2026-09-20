import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const mockRefreshSyncStatus = vi.fn();
const mockPersistSettings = vi.hoisted(() => vi.fn());

vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({
    sync: { refreshSyncStatus: mockRefreshSyncStatus },
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

import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";
import SyncBudgetSettings from "./SyncBudgetSettings.svelte";

const initial = { budget_per_hour: 500 };

function renderSettings(props: Record<string, unknown> = {}) {
  const onUpdate = vi.fn();
  render(SettingsRuntimeHarness, {
    props: { component: SyncBudgetSettings, componentProps: { sync: initial, onUpdate, ...props } },
  });
  const input = screen.getByRole("spinbutton", { name: "Hourly sync budget" }) as HTMLInputElement;
  return { input, onUpdate };
}

describe("SyncBudgetSettings", () => {
  afterEach(() => {
    cleanup();
    mockRefreshSyncStatus.mockReset();
    mockPersistSettings.mockReset();
  });

  it("labels the hub-owned sync budget", () => {
    renderSettings({ owner: "hub" });

    expect(screen.getByText("The sync budget is managed by the fleet hub.")).toBeTruthy();
  });

  it("saves a new budget and refreshes the live ceiling", async () => {
    mockPersistSettings.mockReturnValue(Effect.succeed({ sync: { budget_per_hour: 3000 } }));
    const { input, onUpdate } = renderSettings();

    await fireEvent.input(input, { target: { value: "3000" } });
    await fireEvent.change(input);

    await waitFor(() => expect(onUpdate).toHaveBeenCalledWith({ budget_per_hour: 3000 }));
    const build = mockPersistSettings.mock.calls[0]![0] as () => unknown;
    expect(build()).toEqual({ sync: { budget_per_hour: 3000 } });
    expect(mockRefreshSyncStatus).toHaveBeenCalledTimes(1);
  });

  it.each(["49", "15001", "12.5", ""])("rejects %j without sending a request", async (value) => {
    const { input } = renderSettings();

    await fireEvent.input(input, { target: { value } });
    await fireEvent.change(input);

    expect(screen.getByRole("alert").textContent).toContain("Enter a whole number from 50 to 15000.");
    expect(mockPersistSettings).not.toHaveBeenCalled();
  });

  it("restores the saved budget when the save fails", async () => {
    mockPersistSettings.mockReturnValue(Effect.fail(new Error("offline")));
    const { input, onUpdate } = renderSettings();

    await fireEvent.input(input, { target: { value: "900" } });
    await fireEvent.change(input);

    await waitFor(() => expect(input.value).toBe("500"));
    expect(onUpdate).not.toHaveBeenCalled();
    expect(mockRefreshSyncStatus).not.toHaveBeenCalled();
  });
});
