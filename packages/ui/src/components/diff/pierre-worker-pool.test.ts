import { afterEach, describe, expect, it, vi } from "vite-plus/test";

describe("pierre-worker-pool", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.resetModules();
    delete (globalThis as { Worker?: unknown }).Worker;
    delete (globalThis as { __middlemanForceSyntaxHighlight?: unknown }).__middlemanForceSyntaxHighlight;
  });

  it("uses the shared tokenize line-length cap for worker highlighting", async () => {
    const getOrCreateWorkerPoolSingleton = vi.fn();
    vi.doMock("@pierre/diffs/worker", () => ({
      getOrCreateWorkerPoolSingleton,
    }));
    class TestWorker {}
    (globalThis as { Worker?: unknown }).Worker = TestWorker;

    const { diffTokenizeMaxLineLength, getPierreDiffWorkerPool } = await import("./pierre-worker-pool.js");

    getPierreDiffWorkerPool();

    expect(getOrCreateWorkerPoolSingleton).toHaveBeenCalledWith(
      expect.objectContaining({
        highlighterOptions: expect.objectContaining({
          tokenizeMaxLineLength: diffTokenizeMaxLineLength,
        }),
      }),
    );
  });
});
