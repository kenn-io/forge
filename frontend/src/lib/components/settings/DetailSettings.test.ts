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

const initial = {
  initial_timeline_entry_limit: 50,
  collapse_single_line_breaks: false,
  render_commit_messages_as_markdown: false,
};

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

  it("saves the initial timeline entry limit when the input changes", async () => {
    const onUpdate = vi.fn();
    const saved = { ...initial, initial_timeline_entry_limit: 80 };
    mockPersistSettings.mockReturnValue(Effect.succeed({ detail: saved }));
    render(SettingsRuntimeHarness, {
      props: { component: DetailSettings, componentProps: { detail: initial, onUpdate } },
    });

    const input = screen.getByRole("spinbutton", { name: "Initial timeline entries" });
    expect(input.getAttribute("min")).toBe("10");
    expect(input.getAttribute("max")).toBe("250");
    expect(screen.queryByRole("button", { name: /save/i })).toBeNull();
    await fireEvent.change(input, { target: { value: "80" } });

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ detail: saved });
    expect(onUpdate).toHaveBeenLastCalledWith(saved);
    expect(mockSetDetailSettings).toHaveBeenCalledWith(saved);
  });

  it("accepts any integer in range even when it is not a multiple of the spinner step", async () => {
    const saved = { ...initial, initial_timeline_entry_limit: 11 };
    mockPersistSettings.mockReturnValue(Effect.succeed({ detail: saved }));
    render(SettingsRuntimeHarness, {
      props: { component: DetailSettings, componentProps: { detail: initial, onUpdate: vi.fn() } },
    });

    const input = screen.getByRole("spinbutton", { name: "Initial timeline entries" }) as HTMLInputElement;
    expect(input.getAttribute("step")).toBe("10");
    await fireEvent.input(input, { target: { value: "11" } });
    await fireEvent.change(input, { target: { value: "11" } });

    expect(input.getAttribute("aria-invalid")).toBe("false");
    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ detail: saved });
  });

  it("flags an out-of-range limit inline and sends no request", async () => {
    render(SettingsRuntimeHarness, {
      props: { component: DetailSettings, componentProps: { detail: initial, onUpdate: vi.fn() } },
    });

    const input = screen.getByRole("spinbutton", { name: "Initial timeline entries" }) as HTMLInputElement;
    expect(input.getAttribute("min")).toBe("10");
    expect(input.getAttribute("max")).toBe("250");
    await fireEvent.input(input, { target: { value: "5" } });
    await fireEvent.change(input, { target: { value: "5" } });

    expect(mockPersistSettings).not.toHaveBeenCalled();
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(screen.getByRole("alert").textContent).toContain("from 10 to 250");

    await fireEvent.input(input, { target: { value: "50" } });
    expect(input.getAttribute("aria-invalid")).toBe("false");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("keeps a checkbox click that lands while a limit save is in flight", async () => {
    const onUpdate = vi.fn();
    const afterLimit = { ...initial, initial_timeline_entry_limit: 80 };
    const afterToggle = { ...afterLimit, collapse_single_line_breaks: true };
    let releaseLimit: (() => void) | undefined;
    mockPersistSettings
      .mockReturnValueOnce(
        Effect.promise(
          () =>
            new Promise<{ detail: typeof afterLimit }>((resolve) => {
              releaseLimit = () => resolve({ detail: afterLimit });
            }),
        ),
      )
      .mockReturnValueOnce(Effect.succeed({ detail: afterToggle }));
    render(SettingsRuntimeHarness, {
      props: { component: DetailSettings, componentProps: { detail: initial, onUpdate } },
    });

    const input = screen.getByRole("spinbutton", { name: "Initial timeline entries" });
    await fireEvent.change(input, { target: { value: "80" } });
    const checkbox = screen.getByRole("checkbox", { name: "Collapse single line breaks" }) as HTMLInputElement;
    expect(checkbox.disabled).toBe(false);
    await fireEvent.click(checkbox);

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    releaseLimit?.();
    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledTimes(2));
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ detail: afterLimit });
    expect(mockPersistSettings.mock.calls[1]?.[0]()).toEqual({ detail: afterToggle });
    await waitFor(() => expect(onUpdate).toHaveBeenLastCalledWith(afterToggle));
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
    await fireEvent.change(input, { target: { value: "90" } });

    await waitFor(() => expect(onUpdate).toHaveBeenCalled());
    expect(onUpdate).toHaveBeenLastCalledWith(initial);
    expect(mockSetDetailSettings).not.toHaveBeenCalled();
  });

  it("persists the collapse single line breaks toggle alongside the current limit", async () => {
    const onUpdate = vi.fn();
    const saved = { ...initial, collapse_single_line_breaks: true };
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

  it("persists the commit markdown toggle", async () => {
    const onUpdate = vi.fn();
    const saved = { ...initial, render_commit_messages_as_markdown: true };
    mockPersistSettings.mockReturnValue(Effect.succeed({ detail: saved }));
    render(SettingsRuntimeHarness, {
      props: { component: DetailSettings, componentProps: { detail: initial, onUpdate } },
    });

    await fireEvent.click(screen.getByRole("checkbox", { name: "Render commit messages as markdown" }));

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ detail: saved });
    expect(onUpdate).toHaveBeenLastCalledWith(saved);
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
