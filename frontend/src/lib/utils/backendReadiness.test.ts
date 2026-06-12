import { afterEach, describe, expect, it, vi } from "vite-plus/test";

async function importReadiness(basePath = "/") {
  vi.resetModules();
  window.__BASE_PATH__ = basePath;
  return import("./backendReadiness.js");
}

async function flushMicrotasks(): Promise<void> {
  await Promise.resolve();
}

describe("waitUntilBackendReady", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.useRealTimers();
    window.__BASE_PATH__ = "/";
  });

  it("polls the base-path health endpoint until it is ready", async () => {
    vi.useFakeTimers();
    const fetch = vi.fn().mockResolvedValueOnce({ ok: false }).mockResolvedValueOnce({ ok: true });
    vi.stubGlobal("fetch", fetch);
    const { waitUntilBackendReady } = await importReadiness("/middleman/");

    const ready = waitUntilBackendReady(new AbortController().signal);
    await flushMicrotasks();

    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenNthCalledWith(
      1,
      "/middleman/healthz",
      expect.objectContaining({
        cache: "no-store",
        headers: { Accept: "application/json" },
      }),
    );

    await vi.advanceTimersByTimeAsync(750);
    await ready;

    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("rejects when the caller aborts while waiting between polls", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false }));
    const { waitUntilBackendReady } = await importReadiness();
    const controller = new AbortController();
    const abortReason = new Error("cancelled");

    const ready = waitUntilBackendReady(controller.signal);
    await flushMicrotasks();
    controller.abort(abortReason);

    await expect(ready).rejects.toBe(abortReason);
  });
});
