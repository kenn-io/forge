import { Context, Effect, Layer } from "effect";
import type { Scope } from "effect/Scope";
import { WorkerPoolManager } from "@pierre/diffs/worker";

export const diffTokenizeMaxLineLength = 180;

// Syntax highlighting is the dominant JS cost in diff-heavy e2e runs:
// the shiki worker pool loads ~1MB of worker + wasm per page and
// tokenizes every rendered hunk. Under browser automation diffs render
// as plain text instead, unless a test opts back in by setting
// globalThis.__kenn_forgeForceSyntaxHighlight = true from an init
// script (see diff-highlight-screenshot.spec.ts).
export function syntaxHighlightingDisabledForAutomation(): boolean {
  if (typeof navigator === "undefined" || navigator.webdriver !== true) return false;
  return globalThis.__kenn_forgeForceSyntaxHighlight !== true;
}

function createPierreDiffWorkerPool(): WorkerPoolManager | undefined {
  if (typeof Worker === "undefined") return undefined;
  if (syntaxHighlightingDisabledForAutomation()) return undefined;
  return new WorkerPoolManager(
    {
      workerFactory: () =>
        new Worker(new URL("./pierre-diff-worker-entry.js", import.meta.url), {
          type: "module",
        }),
      poolSize: 4,
      totalASTLRUCacheSize: 200,
    },
    {
      theme: { dark: "pierre-dark", light: "pierre-light" },
      lineDiffType: "word",
      tokenizeMaxLineLength: diffTokenizeMaxLineLength,
    },
  );
}

export const makePierreDiffWorkerPool: Effect.Effect<WorkerPoolManager | undefined, never, Scope> =
  Effect.acquireRelease(Effect.sync(createPierreDiffWorkerPool), (pool) =>
    pool === undefined ? Effect.void : Effect.sync(() => pool.terminate()),
  );

interface PierreDiffWorkerPoolService {
  readonly pool: WorkerPoolManager | undefined;
}

export class PierreDiffWorkerPool extends Context.Service<PierreDiffWorkerPool, PierreDiffWorkerPoolService>()(
  "kenn-forge/PierreDiffWorkerPool",
) {}

export const PierreDiffWorkerPoolLive = Layer.effect(PierreDiffWorkerPool)(
  makePierreDiffWorkerPool.pipe(Effect.map((pool) => ({ pool }))),
);
