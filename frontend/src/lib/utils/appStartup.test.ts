import { afterEach, describe, expect, it, vi } from "vitest";
import { runAppStartup } from "./appStartup.js";
import type { StoreInstances } from "@middleman/ui";
import type { Settings } from "@middleman/ui/api/types";

function makeStores(): StoreInstances {
  return {
    settings: {
      setConfiguredRepos: vi.fn(),
      setTerminalSettings: vi.fn(),
      setTerminalFontFamily: vi.fn(),
      setTerminalRenderer: vi.fn(),
    },
    activity: {
      hydrateDefaults: vi.fn(),
      loadActivity: vi.fn().mockResolvedValue(undefined),
    },
    sync: {
      startPolling: vi.fn(),
    },
    pulls: {
      loadPulls: vi.fn().mockResolvedValue(undefined),
    },
    issues: {
      loadIssues: vi.fn().mockResolvedValue(undefined),
    },
    events: {
      connect: vi.fn(),
      disconnect: vi.fn(),
    },
    // Fields App.svelte doesn't touch during startup — cast to
    // StoreInstances so we don't have to stub every field.
  } as unknown as StoreInstances;
}

function makeSettings(): Settings {
  return {
    repos: [],
    activity: {
      view_mode: "threaded",
      time_range: "7d",
      hide_closed: false,
      hide_bots: false,
      collapse_threads: false,
    },
    terminal: {
      font_family: '"Fira Code", monospace',
      font_size: 14,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      renderer: "xterm",
    },
    agents: [],
  };
}

async function flushMicrotasks(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("runAppStartup", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("runs post-settings side effects on the happy path", async () => {
    const stores = makeStores();
    const settings = makeSettings();
    const onReady = vi.fn();

    runAppStartup({
      getSettings: () => Promise.resolve(settings),
      getStores: () => stores,
      onReady,
    });

    await flushMicrotasks();

    expect(stores.settings.setConfiguredRepos).toHaveBeenCalledWith(
      settings.repos,
    );
    expect(stores.settings.setTerminalSettings).toHaveBeenCalledWith(
      settings.terminal,
    );
    expect(stores.activity.hydrateDefaults).toHaveBeenCalledWith(
      settings.activity,
    );
    expect(onReady).toHaveBeenCalledTimes(1);
    expect(stores.sync.startPolling).toHaveBeenCalledTimes(1);
    expect(stores.pulls.loadPulls).toHaveBeenCalledTimes(1);
    expect(stores.issues.loadIssues).toHaveBeenCalledTimes(1);
    expect(stores.events.connect).toHaveBeenCalledTimes(1);
  });

  it("runs the pre-load hook before marking the app ready and loading lists", async () => {
    const stores = makeStores();
    const beforeInitialLoad = vi.fn();
    const onReady = vi.fn();

    runAppStartup({
      getSettings: () => Promise.resolve(makeSettings()),
      getStores: () => stores,
      beforeInitialLoad,
      onReady,
    });

    await flushMicrotasks();

    expect(beforeInitialLoad).toHaveBeenCalledTimes(1);
    const beforeOrder = beforeInitialLoad.mock.invocationCallOrder[0] ?? 0;
    const readyOrder = onReady.mock.invocationCallOrder[0] ?? 0;
    const pullLoadOrder =
      vi.mocked(stores.pulls.loadPulls).mock.invocationCallOrder[0] ?? 0;
    const issueLoadOrder =
      vi.mocked(stores.issues.loadIssues).mock.invocationCallOrder[0] ?? 0;
    expect(beforeOrder).toBeLessThan(readyOrder);
    expect(beforeOrder).toBeLessThan(pullLoadOrder);
    expect(beforeOrder).toBeLessThan(issueLoadOrder);
  });

  it("skips every post-await side effect when cancelled before settings resolve", async () => {
    const stores = makeStores();
    let resolveSettings: (value: Settings) => void = () => {};
    const settingsPromise = new Promise<Settings>((resolve) => {
      resolveSettings = resolve;
    });
    const onReady = vi.fn();

    const cancel = runAppStartup({
      getSettings: () => settingsPromise,
      getStores: () => stores,
      onReady,
    });

    cancel();
    resolveSettings(makeSettings());
    await flushMicrotasks();

    expect(stores.settings.setConfiguredRepos).not.toHaveBeenCalled();
    expect(stores.settings.setTerminalFontFamily).not.toHaveBeenCalled();
    expect(stores.activity.hydrateDefaults).not.toHaveBeenCalled();
    expect(onReady).not.toHaveBeenCalled();
    expect(stores.sync.startPolling).not.toHaveBeenCalled();
    expect(stores.pulls.loadPulls).not.toHaveBeenCalled();
    expect(stores.issues.loadIssues).not.toHaveBeenCalled();
    expect(stores.events.connect).not.toHaveBeenCalled();
  });

  it("still runs post-await side effects after a rejected getSettings when not cancelled", async () => {
    const stores = makeStores();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const onReady = vi.fn();

    runAppStartup({
      getSettings: () => Promise.reject(new Error("boom")),
      getStores: () => stores,
      onReady,
    });

    await flushMicrotasks();

    expect(warn).toHaveBeenCalled();
    expect(stores.settings.setConfiguredRepos).not.toHaveBeenCalled();
    expect(stores.settings.setTerminalFontFamily).not.toHaveBeenCalled();
    expect(onReady).toHaveBeenCalledTimes(1);
    expect(stores.sync.startPolling).toHaveBeenCalledTimes(1);
    expect(stores.events.connect).toHaveBeenCalledTimes(1);

    warn.mockRestore();
  });

  it("continues startup with defaults when getSettings never settles", async () => {
    vi.useFakeTimers();
    const stores = makeStores();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const onReady = vi.fn();

    runAppStartup({
      getSettings: () => new Promise<Settings>(() => {}),
      getStores: () => stores,
      onReady,
    });

    await vi.advanceTimersByTimeAsync(10_000);

    expect(stores.settings.setConfiguredRepos).not.toHaveBeenCalled();
    expect(stores.settings.setTerminalFontFamily).not.toHaveBeenCalled();
    expect(stores.activity.hydrateDefaults).not.toHaveBeenCalled();
    expect(warn).toHaveBeenCalledWith(
      "Failed to load settings, using defaults:",
      expect.any(Error),
    );
    expect(onReady).toHaveBeenCalledTimes(1);
    expect(stores.sync.startPolling).toHaveBeenCalledTimes(1);
    expect(stores.pulls.loadPulls).toHaveBeenCalledTimes(1);
    expect(stores.issues.loadIssues).toHaveBeenCalledTimes(1);
    expect(stores.events.connect).toHaveBeenCalledTimes(1);
  });

  it("skips post-await side effects when cancelled after a rejected getSettings", async () => {
    const stores = makeStores();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    let rejectSettings: (err: Error) => void = () => {};
    const settingsPromise = new Promise<Settings>((_, reject) => {
      rejectSettings = reject;
    });
    const onReady = vi.fn();

    const cancel = runAppStartup({
      getSettings: () => settingsPromise,
      getStores: () => stores,
      onReady,
    });

    cancel();
    rejectSettings(new Error("boom"));
    await flushMicrotasks();

    expect(onReady).not.toHaveBeenCalled();
    expect(stores.sync.startPolling).not.toHaveBeenCalled();
    expect(stores.events.connect).not.toHaveBeenCalled();

    warn.mockRestore();
  });
});
