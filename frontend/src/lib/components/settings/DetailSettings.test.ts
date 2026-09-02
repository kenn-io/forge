import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const mockSetDetailSettings = vi.fn();
const mockPersistSettings = vi.hoisted(() => vi.fn());

vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({
    settings: { setDetailSettings: mockSetDetailSettings },
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

import DetailSettings from "./DetailSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

const initial = { initial_timeline_entry_limit: 50, collapse_single_line_breaks: false };

describe("DetailSettings", () => {
  afterEach(() => {
    cleanup();
    mockSetDetailSettings.mockReset();
    mockPersistSettings.mockReset();
  });

  it("labels hub-owned detail policy", () => {
    render(SettingsRuntimeHarness, {
      props: {
        component: DetailSettings,
        componentProps: { detail: initial, onUpdate: vi.fn(), owner: "hub" },
      },
    });

    expect(screen.getByText("Detail policy is managed by the fleet hub.")).toBeTruthy();
  });

  it("saves the initial timeline entry limit", async () => {
    const onUpdate = vi.fn();
    const saved = { initial_timeline_entry_limit: 80, collapse_single_line_breaks: false };
    mockPersistSettings.mockReturnValue(Effect.succeed({ detail: saved }));
    render(SettingsRuntimeHarness, {
      props: { component: DetailSettings, componentProps: { detail: initial, onUpdate } },
    });

    const input = screen.getByRole("spinbutton", { name: "Initial timeline entries" });
    expect(input.getAttribute("min")).toBe("10");
    expect(input.getAttribute("max")).toBe("250");
    await fireEvent.input(input, { target: { value: "80" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save timeline limit" }));

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ detail: saved });
    expect(onUpdate).toHaveBeenLastCalledWith(saved);
    expect(mockSetDetailSettings).toHaveBeenCalledWith(saved);
  });

  it("restores the prior limit when saving fails", async () => {
    const onUpdate = vi.fn();
    mockPersistSettings.mockReturnValue(
      Effect.fail({ _tag: "TransientTransportError", operation: "save settings", cause: new Error("save failed") }),
    );
    render(SettingsRuntimeHarness, {
      props: { component: DetailSettings, componentProps: { detail: initial, onUpdate } },
    });

    const input = screen.getByRole("spinbutton", { name: "Initial timeline entries" });
    await fireEvent.input(input, { target: { value: "90" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save timeline limit" }));

    await waitFor(() => expect(onUpdate).toHaveBeenCalled());
    expect(onUpdate).toHaveBeenLastCalledWith(initial);
    expect(mockSetDetailSettings).not.toHaveBeenCalled();
  });

  it("persists the collapse single line breaks toggle alongside the current limit", async () => {
    const onUpdate = vi.fn();
    const saved = { initial_timeline_entry_limit: 50, collapse_single_line_breaks: true };
    mockPersistSettings.mockReturnValue(Effect.succeed({ detail: saved }));
    render(SettingsRuntimeHarness, {
      props: { component: DetailSettings, componentProps: { detail: initial, onUpdate } },
    });

    const checkbox = screen.getByRole("checkbox", { name: "Collapse single line breaks" });
    expect((checkbox as HTMLInputElement).checked).toBe(false);
    await fireEvent.click(checkbox);

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ detail: saved });
    expect(onUpdate).toHaveBeenLastCalledWith(saved);
    expect(mockSetDetailSettings).toHaveBeenCalledWith(saved);
  });

  it("unchecks the toggle again when saving fails", async () => {
    const onUpdate = vi.fn();
    mockPersistSettings.mockReturnValue(
      Effect.fail({ _tag: "TransientTransportError", operation: "save settings", cause: new Error("save failed") }),
    );
    render(SettingsRuntimeHarness, {
      props: { component: DetailSettings, componentProps: { detail: initial, onUpdate } },
    });

    const checkbox = screen.getByRole("checkbox", { name: "Collapse single line breaks" });
    await fireEvent.click(checkbox);

    await waitFor(() => expect(onUpdate).toHaveBeenLastCalledWith(initial));
    await waitFor(() => expect((checkbox as HTMLInputElement).checked).toBe(false));
    expect(mockSetDetailSettings).not.toHaveBeenCalled();
  });
});
