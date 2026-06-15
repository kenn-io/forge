// Unmounting the app shell must stop the sync polling interval. runAppStartup
// starts it after readiness, and a leaked interval keeps firing across tests
// against whichever fetch stub is current (and keeps workers alive). Guards
// the stopPolling() call wired into stopFullAppShell.
//
// Note: this test wraps the global interval timers but never uses waitFor
// (which itself polls on setInterval and would be counted as a live timer).
// Svelte runs onDestroy synchronously on unmount, so the post-unmount
// assertion is synchronous.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { installAppDomGlobals, mountApp, resetKeyboardModuleState } from "./test/appHarness.js";

describe("app shell teardown", () => {
  vi.setConfig({ testTimeout: 20_000 });

  let cleanupTimers = (): void => {};

  beforeEach(() => {
    installAppDomGlobals();
  });

  afterEach(async () => {
    cleanupTimers();
    vi.unstubAllGlobals();
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("stops sync polling on unmount so no interval outlives the test", async () => {
    const live = new Set<ReturnType<typeof setInterval>>();
    const realSet = globalThis.setInterval.bind(globalThis);
    const realClear = globalThis.clearInterval.bind(globalThis);
    vi.stubGlobal("setInterval", (...args: Parameters<typeof setInterval>) => {
      const handle = realSet(...args);
      live.add(handle);
      return handle;
    });
    vi.stubGlobal("clearInterval", (handle: Parameters<typeof clearInterval>[0]) => {
      if (handle !== undefined) live.delete(handle as ReturnType<typeof setInterval>);
      return realClear(handle);
    });
    cleanupTimers = () => live.forEach((h) => realClear(h));

    const app = await mountApp("/pulls");
    // Let startup run (readiness -> settings -> onReady -> sync.startPolling).
    // Real setTimeout is left unstubbed, so this is a genuine settle.
    await new Promise((resolve) => setTimeout(resolve, 1500));
    expect(live.size).toBeGreaterThan(0);

    // onDestroy runs synchronously: stopFullAppShell -> sync.stopPolling +
    // each component effect's interval cleanup -> every interval cleared.
    app.unmount();
    expect(live.size).toBe(0);
  });
});
