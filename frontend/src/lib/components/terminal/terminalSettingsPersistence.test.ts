import { DEFAULT_TERMINAL_SETTINGS, type TerminalSettings } from "../../api/types.js";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import type { StartupSnapshot } from "../../app/startup-workflow.js";
import {
  beginTerminalSettingsHydration,
  hydrateTerminalSettings,
  previewTerminalSettings,
  restoreTerminalSettingsPreview,
  saveTerminalSettings as saveTerminalSettingsEffect,
} from "../../stores/terminal-settings-persistence.js";

let runtime: OwnedAppRuntime;
let persistSettings: (settings: TerminalSettings) => Promise<TerminalSettings>;
let persistedTerminal: TerminalSettings;

function settingsResponse(terminal: TerminalSettings): StartupSnapshot {
  return {
    activity: {
      view_mode: "threaded",
      time_range: "7d",
      hide_closed: false,
      hide_bots: false,
      collapse_threads: false,
      default_branch_retention_days: 90,
      default_branch_max_commits: 5000,
    },
    agents: [],
    fleet: {
      enabled: false,
      sessions: {},
      peers: [],
      ssh_peers: [],
      restart_required: false,
    },
    issues: { hide_bots: true },
    kata_projects: [],
    launch_targets: [],
    modes: {
      activity: true,
      repos: true,
      kata: false,
      docs: false,
      pulls: true,
      issues: true,
      reviews: true,
      workspaces: true,
    },
    notifications: { enabled: true },
    pull_requests: {
      allow_mid_stack_merges: false,
      prefer_github_native_stacks: false,
    },
    repos: [],
    terminal,
    workspaces: { auto_assign_on_create: false },
  };
}

function saveTerminalSettings(options: {
  baseline: TerminalSettings;
  changes: Partial<TerminalSettings>;
  persist: (settings: TerminalSettings) => Promise<TerminalSettings>;
  store: ReturnType<typeof createStore>;
}): Promise<TerminalSettings> {
  persistSettings = options.persist;
  const execution = runtime.runCommand(
    saveTerminalSettingsEffect({
      baseline: options.baseline,
      changes: options.changes,
      store: options.store,
    }),
    {
      operation: "test terminal settings persistence",
      safeContext: {},
      onFailure: () => undefined,
    },
  );
  return Effect.runPromise(execution.await.pipe(Effect.flatMap((exit) => exit)));
}

beforeEach(() => {
  runtime = makeAppRuntime();
  persistSettings = (settings) => Promise.resolve(settings);
  persistedTerminal = { ...DEFAULT_TERMINAL_SETTINGS };
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const request = input instanceof Request ? input : new Request(input, init);
    if (request.method === "GET") {
      return Response.json(settingsResponse(persistedTerminal));
    }
    const body = await request.clone().json();
    if (
      typeof body !== "object" ||
      body === null ||
      !("terminal" in body) ||
      typeof body.terminal !== "object" ||
      body.terminal === null
    ) {
      return Response.json({ detail: "invalid terminal settings" }, { status: 400 });
    }
    const terminal = body.terminal as TerminalSettings;
    persistedTerminal = await persistSettings(terminal);
    return Response.json(settingsResponse(persistedTerminal));
  };
  vi.stubGlobal("fetch", fetch);
});

afterEach(async () => {
  await Effect.runPromise(runtime.disposeEffect);
  vi.unstubAllGlobals();
});

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
    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(1));

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

    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(1));
    firstSave.reject(new Error("settings unavailable"));
    await expect(firstZoom).rejects.toMatchObject({ _tag: "TransientTransportError" });
    await secondZoom;

    expect(store.getTerminalSettings().font_size).toBe(14);
    expect(persist).toHaveBeenNthCalledWith(2, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_size: 14,
    });
  });

  it("does not let an older rejection reclaim a field after an ABA change", async () => {
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
    const thirdZoom = saveTerminalSettings({
      baseline: store.getTerminalSettings(),
      changes: { font_size: 13 },
      persist,
      store,
    });

    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(1));
    firstSave.reject(new Error("settings unavailable"));
    await expect(firstZoom).rejects.toMatchObject({ _tag: "TransientTransportError" });
    await Promise.all([secondZoom, thirdZoom]);

    expect(persist).toHaveBeenNthCalledWith(3, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_size: 13,
    });
    expect(store.getTerminalSettings().font_size).toBe(13);
  });

  it("rolls consecutive rejected saves back to the last confirmed value", async () => {
    const firstSave = deferred<TerminalSettings>();
    const secondSave = deferred<TerminalSettings>();
    const store = createStore();
    const persist = vi
      .fn<(settings: TerminalSettings) => Promise<TerminalSettings>>()
      .mockImplementationOnce(() => firstSave.promise)
      .mockImplementationOnce(() => secondSave.promise);

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

    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(1));
    firstSave.reject(new Error("first save failed"));
    await expect(firstZoom).rejects.toMatchObject({ _tag: "TransientTransportError" });
    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(2));
    secondSave.reject(new Error("second save failed"));
    await expect(secondZoom).rejects.toMatchObject({ _tag: "TransientTransportError" });

    expect(store.getTerminalSettings().font_size).toBe(DEFAULT_TERMINAL_SETTINGS.font_size);
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

    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(1));
    firstSave.reject(new Error("settings unavailable"));
    await expect(optionsSave).rejects.toMatchObject({ _tag: "TransientTransportError" });
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
      scrollback: 2000,
    });
  });

  it("preserves fields mutated after a settings hydration begins", async () => {
    const pendingZoom = deferred<TerminalSettings>();
    const store = createStore();
    const persist = vi
      .fn<(settings: TerminalSettings) => Promise<TerminalSettings>>()
      .mockImplementationOnce(() => pendingZoom.promise)
      .mockImplementation(async (settings) => settings);
    const hydration = beginTerminalSettingsHydration(store);
    const zoomSave = saveTerminalSettings({
      baseline: store.getTerminalSettings(),
      changes: { font_size: 13 },
      persist,
      store,
    });

    hydrateTerminalSettings(hydration, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Hydrated Font", monospace',
    });

    expect(store.getTerminalSettings()).toEqual({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Hydrated Font", monospace',
      font_size: 13,
    });

    pendingZoom.resolve({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_size: 13,
    });
    await zoomSave;
    await saveTerminalSettings({
      baseline: store.getTerminalSettings(),
      changes: { scrollback: 2000 },
      persist,
      store,
    });

    expect(persist).toHaveBeenNthCalledWith(2, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Hydrated Font", monospace',
      font_size: 13,
      scrollback: 2000,
    });
  });

  it("preserves queued zooms when hydration begins after their optimistic changes", async () => {
    const firstSave = deferred<TerminalSettings>();
    const secondSave = deferred<TerminalSettings>();
    const store = createStore();
    const persist = vi
      .fn<(settings: TerminalSettings) => Promise<TerminalSettings>>()
      .mockImplementationOnce(() => firstSave.promise)
      .mockImplementationOnce(() => secondSave.promise);

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
    const hydration = beginTerminalSettingsHydration(store);

    hydrateTerminalSettings(hydration, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Hydrated Font", monospace',
    });

    expect(store.getTerminalSettings()).toEqual({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Hydrated Font", monospace',
      font_size: 14,
    });

    firstSave.resolve({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_size: 13,
    });
    await firstZoom;
    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(2));
    expect(persist).toHaveBeenNthCalledWith(2, {
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Hydrated Font", monospace',
      font_size: 14,
    });
    secondSave.resolve({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Hydrated Font", monospace',
      font_size: 14,
    });
    await secondZoom;

    expect(store.getTerminalSettings()).toEqual({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_family: '"Hydrated Font", monospace',
      font_size: 14,
    });
  });

  it("rebases an active preview when hydration confirms its draft value", async () => {
    const store = createStore();
    const baseline = store.getTerminalSettings();

    previewTerminalSettings(store, baseline, {
      ...baseline,
      font_size: 13,
    });
    const hydration = beginTerminalSettingsHydration(store);

    hydrateTerminalSettings(hydration, {
      ...baseline,
      font_size: 13,
    });
    previewTerminalSettings(store, baseline, {
      ...baseline,
      font_size: 13,
      scrollback: 2000,
    });
    restoreTerminalSettingsPreview(store);

    expect(store.getTerminalSettings()).toEqual({
      ...baseline,
      font_size: 13,
    });

    const persist = vi.fn(async (settings: TerminalSettings) => settings);
    await saveTerminalSettings({
      baseline,
      changes: { scrollback: 2000 },
      persist,
      store,
    });
    expect(persist).toHaveBeenCalledWith({
      ...baseline,
      font_size: 13,
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
