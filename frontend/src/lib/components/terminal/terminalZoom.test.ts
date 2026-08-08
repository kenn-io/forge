import { DEFAULT_TERMINAL_SETTINGS, type TerminalSettings } from "../../api/types.js";
import { describe, expect, it, vi } from "vite-plus/test";
import {
  MAX_TERMINAL_FONT_SIZE,
  MIN_TERMINAL_FONT_SIZE,
  RESET_TERMINAL_FONT_SIZE,
  createTerminalZoomController,
} from "./terminalZoom";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

function createHarness(
  persist: (settings: TerminalSettings) => Promise<TerminalSettings> = async (settings) => settings,
) {
  let settings = { ...DEFAULT_TERMINAL_SETTINGS };
  const reportError = vi.fn();
  const store = {
    getTerminalSettings: () => settings,
    setTerminalSettings: (next: TerminalSettings) => {
      settings = next;
    },
  };
  const controller = createTerminalZoomController({
    persist,
    reportError,
    store,
  });
  return {
    controller,
    getSettings: () => settings,
    reportError,
  };
}

describe("terminal zoom controller", () => {
  it("uses the shared 12px terminal default as its reset target", () => {
    expect(DEFAULT_TERMINAL_SETTINGS.font_size).toBe(12);
    expect(RESET_TERMINAL_FONT_SIZE).toBe(DEFAULT_TERMINAL_SETTINGS.font_size);
  });

  it("updates the shared settings immediately and clamps persisted font sizes", async () => {
    const persist = vi.fn(async (settings: TerminalSettings) => settings);
    const harness = createHarness(persist);

    harness.controller.setFontSize(MAX_TERMINAL_FONT_SIZE + 10);
    expect(harness.getSettings().font_size).toBe(MAX_TERMINAL_FONT_SIZE);
    await harness.controller.whenIdle();
    expect(persist).toHaveBeenLastCalledWith(expect.objectContaining({ font_size: MAX_TERMINAL_FONT_SIZE }));

    harness.controller.setFontSize(MIN_TERMINAL_FONT_SIZE - 10);
    expect(harness.getSettings().font_size).toBe(MIN_TERMINAL_FONT_SIZE);
    await harness.controller.whenIdle();
    expect(persist).toHaveBeenLastCalledWith(expect.objectContaining({ font_size: MIN_TERMINAL_FONT_SIZE }));
  });

  it("serializes rapid saves without letting an older response overwrite the latest zoom", async () => {
    const first = deferred<TerminalSettings>();
    const second = deferred<TerminalSettings>();
    const persist = vi
      .fn<(settings: TerminalSettings) => Promise<TerminalSettings>>()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const harness = createHarness(persist);

    harness.controller.increase();
    harness.controller.increase();
    expect(harness.getSettings().font_size).toBe(14);
    await Promise.resolve();
    expect(persist).toHaveBeenCalledTimes(1);

    first.resolve({ ...DEFAULT_TERMINAL_SETTINGS, font_size: 13 });
    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(2));
    expect(harness.getSettings().font_size).toBe(14);

    second.resolve({ ...DEFAULT_TERMINAL_SETTINGS, font_size: 14 });
    await harness.controller.whenIdle();
    expect(harness.getSettings().font_size).toBe(14);
  });

  it("rolls back the latest failed save to the last confirmed font size", async () => {
    const persist = vi.fn(async () => {
      throw new Error("settings unavailable");
    });
    const harness = createHarness(persist);

    harness.controller.increase();
    expect(harness.getSettings().font_size).toBe(13);
    await harness.controller.whenIdle();

    expect(harness.getSettings().font_size).toBe(RESET_TERMINAL_FONT_SIZE);
    expect(harness.reportError).toHaveBeenCalledWith(expect.objectContaining({ message: "settings unavailable" }));
  });
});
