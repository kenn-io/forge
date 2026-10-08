import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { STORES_KEY } from "../../context.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";
import { beginACPSettingsHydration, hydrateACPSettings } from "../../stores/acp-settings-persistence.js";
import ACPSettings from "./ACPSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

const persist = vi.hoisted(() => vi.fn());
vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  const { Layer } = await import("effect");
  return { ...actual, SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({ persist }) };
});

let settings: ReturnType<typeof createSettingsStore>;
beforeEach(() => {
  settings = createSettingsStore();
  persist.mockReset();
});
afterEach(cleanup);

function mount() {
  render(SettingsRuntimeHarness, {
    props: { component: ACPSettings, componentProps: {} },
    context: new Map([[STORES_KEY, { settings }]]),
  });
}

describe("ACP appearance", () => {
  it("saves a font family on change and applies only the acknowledged settings", async () => {
    const staleRead = beginACPSettingsHydration(settings);
    let release: (() => void) | undefined;
    persist.mockReturnValue(
      Effect.promise(
        () =>
          new Promise((resolve) => {
            release = () => resolve({ acp: { font_family: "MesloLGS NF", font_size: 13 } });
          }),
      ),
    );
    mount();
    await fireEvent.input(screen.getByRole("textbox", { name: "Font family" }), {
      target: { value: "  MesloLGS NF  " },
    });
    await fireEvent.change(screen.getByRole("textbox", { name: "Font family" }), {
      target: { value: "  MesloLGS NF  " },
    });
    await waitFor(() => expect(persist).toHaveBeenCalledOnce());
    expect(persist.mock.calls[0]?.[0]()).toEqual({ acp: { font_family: "MesloLGS NF" } });
    expect(settings.getACPSettings()).toEqual({ font_family: "", font_size: 13 });
    hydrateACPSettings(staleRead, { font_family: "stale", font_size: 8 });
    expect(settings.getACPSettings().font_family).toBe("");
    const pendingRead = beginACPSettingsHydration(settings);
    release?.();
    await waitFor(() => expect(settings.getACPSettings()).toEqual({ font_family: "MesloLGS NF", font_size: 13 }));
    hydrateACPSettings(pendingRead, { font_family: "stale", font_size: 8 });
    expect(settings.getACPSettings()).toEqual({ font_family: "MesloLGS NF", font_size: 13 });
    expect(settings.getTerminalSettings().font_family).toBe("");
  });

  it("saves only the size field and restores defaults when the font is cleared", async () => {
    settings.setACPSettings({ font_family: "MesloLGS NF", font_size: 13 });
    persist
      .mockReturnValueOnce(Effect.succeed({ acp: { font_family: "MesloLGS NF", font_size: 19 } }))
      .mockReturnValueOnce(Effect.succeed({ acp: { font_family: "", font_size: 19 } }));
    mount();
    await fireEvent.input(screen.getByRole("spinbutton", { name: "Font size (px)" }), { target: { value: "19" } });
    await fireEvent.change(screen.getByRole("spinbutton", { name: "Font size (px)" }), { target: { value: "19" } });
    await waitFor(() => expect(settings.getACPSettings().font_size).toBe(19));
    await fireEvent.input(screen.getByRole("textbox", { name: "Font family" }), { target: { value: "" } });
    await fireEvent.change(screen.getByRole("textbox", { name: "Font family" }), { target: { value: "" } });
    await waitFor(() => expect(settings.getACPSettings().font_family).toBe(""));
    expect(persist.mock.calls.map(([request]) => request())).toEqual([
      { acp: { font_size: 19 } },
      { acp: { font_family: "" } },
    ]);
  });

  it("preserves a newer size edit when a font-family save completes", async () => {
    let release: (() => void) | undefined;
    persist
      .mockReturnValueOnce(
        Effect.promise(
          () =>
            new Promise((resolve) => {
              release = () => resolve({ acp: { font_family: "serif", font_size: 13 } });
            }),
        ),
      )
      .mockReturnValueOnce(Effect.succeed({ acp: { font_family: "serif", font_size: 20 } }));
    mount();
    const family = screen.getByRole("textbox", { name: "Font family" });
    const size = screen.getByRole("spinbutton", { name: "Font size (px)" }) as HTMLInputElement;
    await fireEvent.input(family, { target: { value: "serif" } });
    await fireEvent.change(family);
    await waitFor(() => expect(persist).toHaveBeenCalledOnce());
    await fireEvent.input(size, { target: { value: "20" } });
    release?.();
    await waitFor(() => expect(settings.getACPSettings().font_family).toBe("serif"));
    expect(size.value).toBe("20");
    await fireEvent.change(size);
    await waitFor(() => expect(settings.getACPSettings().font_size).toBe(20));
    expect(persist.mock.calls[1]?.[0]()).toEqual({ acp: { font_size: 20 } });
  });

  it("preserves a later edit to the same field and follows external updates once it is saved", async () => {
    let release: (() => void) | undefined;
    persist
      .mockReturnValueOnce(
        Effect.promise(
          () =>
            new Promise((resolve) => {
              release = () => resolve({ acp: { font_family: "serif", font_size: 13 } });
            }),
        ),
      )
      .mockReturnValueOnce(Effect.succeed({ acp: { font_family: "monospace", font_size: 13 } }));
    mount();
    const family = screen.getByRole("textbox", { name: "Font family" }) as HTMLInputElement;
    await fireEvent.input(family, { target: { value: "serif" } });
    await fireEvent.change(family);
    await waitFor(() => expect(persist).toHaveBeenCalledOnce());
    await fireEvent.input(family, { target: { value: "monospace" } });
    release?.();
    await waitFor(() => expect(settings.getACPSettings().font_family).toBe("serif"));
    expect(family.value).toBe("monospace");
    await fireEvent.change(family);
    await waitFor(() => expect(settings.getACPSettings().font_family).toBe("monospace"));
    settings.setACPSettings({ font_family: "sans-serif", font_size: 18 });
    await waitFor(() => expect(family.value).toBe("sans-serif"));
    expect((screen.getByRole("spinbutton", { name: "Font size (px)" }) as HTMLInputElement).value).toBe("18");
  });

  it.each(["", "7", "33", "13.5"])("rejects invalid size %s without saving", async (value) => {
    mount();
    const input = screen.getByRole("spinbutton", { name: "Font size (px)" });
    await fireEvent.input(input, { target: { value } });
    await fireEvent.change(input, { target: { value } });
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(screen.getByRole("alert").textContent).toContain("from 8 to 32");
    expect(persist).not.toHaveBeenCalled();
  });

  it("keeps the saved preference after a rejected change", async () => {
    persist.mockReturnValue(
      Effect.fail({ _tag: "TransientTransportError", operation: "save settings", cause: new Error("offline") }),
    );
    mount();
    const input = screen.getByRole("spinbutton", { name: "Font size (px)" }) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "20" } });
    await fireEvent.change(input, { target: { value: "20" } });
    await waitFor(() => expect(persist).toHaveBeenCalledOnce());
    await waitFor(() => expect(input.value).toBe("13"));
    expect(settings.getACPSettings()).toEqual({ font_family: "", font_size: 13 });
  });
});
