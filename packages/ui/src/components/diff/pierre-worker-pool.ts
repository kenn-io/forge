import {
  getOrCreateWorkerPoolSingleton,
  type WorkerPoolManager,
} from "@pierre/diffs/worker";

export function getPierreDiffWorkerPool(): WorkerPoolManager | undefined {
  if (typeof Worker === "undefined") return undefined;
  return getOrCreateWorkerPoolSingleton({
    poolOptions: {
      workerFactory: () =>
        new Worker(new URL("@pierre/diffs/worker/worker.js", import.meta.url), {
          type: "module",
        }),
      poolSize: 4,
      totalASTLRUCacheSize: 200,
    },
    highlighterOptions: {
      theme: { dark: "pierre-dark", light: "pierre-light" },
      lineDiffType: "word",
      tokenizeMaxLineLength: 2_000,
      langs: [
        "bash",
        "css",
        "go",
        "html",
        "javascript",
        "json",
        "markdown",
        "sql",
        "toml",
        "typescript",
        "yaml",
      ],
    },
  });
}
