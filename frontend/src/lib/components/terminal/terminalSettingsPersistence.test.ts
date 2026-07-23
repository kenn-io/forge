import { DEFAULT_TERMINAL_SETTINGS, type TerminalSettings } from "@middleman/ui/api/types";
import { describe, expect, it, vi } from "vite-plus/test";
import { saveTerminalSettings } from "./terminalSettingsPersistence";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

function createStore() {
  let settings = { ...DEFAULT_TERMINAL_SETTINGS };
  return {
    getTerminalSettings: () => settings,
    setTerminalSettings: (next: TerminalSettings) => {
      settings = next;
    },
  };
}

describe("terminal settings persistence", () => {
  it("serializes options and zoom saves without dropping either change", async () => {
    const firstSave = deferred<TerminalSettings>();
    const store = createStore();
    const persist = vi
      .fn<(settings: TerminalSettings) => Promise<TerminalSettings>>()
      .mockImplementationOnce(() => firstSave.promise)
      .mockImplementation(async (settings) => settings);

    const optionsSave = saveTerminalSettings({
      baseline: store.getTerminalSettings(),
      changes: { font_family: '"Iosevka Term", monospace' },
      persist,
      store,
    });
    const zoomSave = saveTerminalSettings({
      baseline: store.getTerminalSettings(),
      changes: { font_size: 13 },
      persist,
      store,
    });

    expect(store.getTerminalSettings()).toEqual({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Iosevka Term", monospace',
      font_size: 13,
    });
    await Promise.resolve();
    expect(persist).toHaveBeenCalledTimes(1);

    firstSave.resolve({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Iosevka Term", monospace',
    });
    await Promise.all([optionsSave, zoomSave]);

    expect(persist).toHaveBeenNthCalledWith(2, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Iosevka Term", monospace',
      font_size: 13,
    });
    expect(store.getTerminalSettings()).toEqual({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Iosevka Term", monospace',
      font_size: 13,
    });
  });

  it("does not roll a newer optimistic value back when an older save fails", async () => {
    const firstSave = deferred<TerminalSettings>();
    const store = createStore();
    const persist = vi
      .fn<(settings: TerminalSettings) => Promise<TerminalSettings>>()
      .mockImplementationOnce(() => firstSave.promise)
      .mockImplementation(async (settings) => settings);

    const firstZoom = saveTerminalSettings({
      baseline: store.getTerminalSettings(),
      changes: { font_size: 13 },
      persist,
      store,
    });
    const secondZoom = saveTerminalSettings({
      baseline: store.getTerminalSettings(),
      changes: { font_size: 14 },
      persist,
      store,
    });

    firstSave.reject(new Error("settings unavailable"));
    await expect(firstZoom).rejects.toThrow("settings unavailable");
    await secondZoom;

    expect(store.getTerminalSettings().font_size).toBe(14);
    expect(persist).toHaveBeenNthCalledWith(2, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_size: 14,
    });
  });
});
