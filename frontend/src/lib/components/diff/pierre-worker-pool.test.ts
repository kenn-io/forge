import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { Effect } from "effect";

describe("pierre-worker-pool", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.resetModules();
    delete (globalThis as { Worker?: unknown }).Worker;
    delete (globalThis as { __kenn_forgeForceSyntaxHighlight?: unknown }).__kenn_forgeForceSyntaxHighlight;
  });

  it("uses the shared tokenize line-length cap for worker highlighting", async () => {
    const terminate = vi.fn();
    const WorkerPoolManager = vi.fn(function WorkerPoolManager() {
      return { terminate };
    });
    vi.doMock("@pierre/diffs/worker", () => ({
      WorkerPoolManager,
    }));
    class TestWorker {}
    (globalThis as { Worker?: unknown }).Worker = TestWorker;

    const { diffTokenizeMaxLineLength, PierreDiffWorkerPool, PierreDiffWorkerPoolLive } =
      await import("./pierre-worker-pool.js");

    await Effect.runPromise(
      Effect.gen(function* () {
        const workerPool = yield* PierreDiffWorkerPool;
        const first = yield* Effect.scoped(Effect.succeed(workerPool.pool));
        expect(first).toBeDefined();
        expect(terminate).not.toHaveBeenCalled();

        const second = yield* Effect.scoped(Effect.succeed(workerPool.pool));
        expect(second).toBe(first);
        expect(terminate).not.toHaveBeenCalled();
      }).pipe(Effect.provide(PierreDiffWorkerPoolLive)),
    );

    expect(WorkerPoolManager).toHaveBeenCalledWith(
      expect.objectContaining({
        workerFactory: expect.any(Function),
        poolSize: 4,
        totalASTLRUCacheSize: 200,
      }),
      expect.objectContaining({
        tokenizeMaxLineLength: diffTokenizeMaxLineLength,
      }),
    );
    expect(terminate).toHaveBeenCalledOnce();
  });
});
