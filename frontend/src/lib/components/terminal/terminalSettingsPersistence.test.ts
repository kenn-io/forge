import { DEFAULT_TERMINAL_SETTINGS, type TerminalSettings } from "@middleman/ui/api/types";
import { describe, expect, it, vi } from "vite-plus/test";
import { previewTerminalSettings, saveTerminalSettings } from "./terminalSettingsPersistence";

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
    const baseline = store.getTerminalSettings();

    previewTerminalSettings(store, baseline, {
      ...baseline,
      font_family: '"Iosevka Term", monospace',
    });
    const optionsSave = saveTerminalSettings({
      baseline,
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

  it("excludes a rejected options preview from a queued zoom save", async () => {
    const firstSave = deferred<TerminalSettings>();
    const store = createStore();
    const baseline = store.getTerminalSettings();
    const previewedFontFamily = '"Iosevka Term", monospace';
    const persist = vi
      .fn<(settings: TerminalSettings) => Promise<TerminalSettings>>()
      .mockImplementationOnce(() => firstSave.promise)
      .mockImplementation(async (settings) => settings);

    previewTerminalSettings(store, baseline, {
      ...baseline,
      font_family: previewedFontFamily,
    });
    const optionsSave = saveTerminalSettings({
      baseline,
      changes: { font_family: previewedFontFamily },
      persist,
      store,
    });
    const zoomSave = saveTerminalSettings({
      baseline: store.getTerminalSettings(),
      changes: { font_size: 13 },
      persist,
      store,
    });

    await Promise.resolve();
    firstSave.reject(new Error("settings unavailable"));
    await expect(optionsSave).rejects.toThrow("settings unavailable");
    await zoomSave;

    expect(persist).toHaveBeenNthCalledWith(2, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_size: 13,
    });
  });

  it("keeps the composite confirmation for a later save from a stale component baseline", async () => {
    const firstSave = deferred<TerminalSettings>();
    const store = createStore();
    const persist = vi
      .fn<(settings: TerminalSettings) => Promise<TerminalSettings>>()
      .mockImplementationOnce(() => firstSave.promise)
      .mockImplementation(async (settings) => settings);
    const baseline = store.getTerminalSettings();

    previewTerminalSettings(store, baseline, {
      ...baseline,
      font_family: '"Iosevka Term", monospace',
    });
    const optionsSave = saveTerminalSettings({
      baseline,
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
    firstSave.resolve({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Iosevka Term", monospace',
    });
    const staleOptionsBaseline = await optionsSave;
    await zoomSave;

    await saveTerminalSettings({
      baseline: staleOptionsBaseline,
      changes: { scrollback: 2000 },
      persist,
      store,
    });

    expect(persist).toHaveBeenNthCalledWith(3, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Iosevka Term", monospace',
      font_size: 13,
      scrollback: 2000,
    });
    expect(store.getTerminalSettings()).toEqual({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Iosevka Term", monospace',
      font_size: 13,
      scrollback: 2000,
    });
  });

  it("rebases an idle queue on authoritative settings from the store", async () => {
    const store = createStore();
    const persist = vi.fn(async (settings: TerminalSettings) => settings);
    const staleBaseline = store.getTerminalSettings();

    await saveTerminalSettings({
      baseline: staleBaseline,
      changes: { font_size: 13 },
      persist,
      store,
    });

    store.setTerminalSettings({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Iosevka Term", monospace',
      font_size: 17,
      renderer: "ghostty-web",
    });

    await saveTerminalSettings({
      baseline: staleBaseline,
      changes: { scrollback: 2000 },
      persist,
      store,
    });

    expect(persist).toHaveBeenNthCalledWith(2, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Iosevka Term", monospace',
      font_size: 17,
      renderer: "ghostty-web",
      scrollback: 2000,
    });
  });

  it("restores a previously previewed field when the draft returns to its baseline", () => {
    const store = createStore();
    const baseline = store.getTerminalSettings();

    previewTerminalSettings(store, baseline, {
      ...baseline,
      font_size: 20,
    });
    expect(store.getTerminalSettings().font_size).toBe(20);
    store.setTerminalSettings({
      ...store.getTerminalSettings(),
      scrollback: 2000,
    });

    previewTerminalSettings(store, baseline, baseline);

    expect(store.getTerminalSettings()).toEqual({
      ...baseline,
      scrollback: 2000,
    });
  });
});
