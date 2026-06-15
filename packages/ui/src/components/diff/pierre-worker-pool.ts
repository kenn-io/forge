import { getOrCreateWorkerPoolSingleton, type WorkerPoolManager } from "@pierre/diffs/worker";

export const diffTokenizeMaxLineLength = 180;

// Syntax highlighting is the dominant JS cost in diff-heavy e2e runs:
// the shiki worker pool loads ~1MB of worker + wasm per page and
// tokenizes every rendered hunk. Under browser automation diffs render
// as plain text instead, unless a test opts back in by setting
// globalThis.__middlemanForceSyntaxHighlight = true from an init
// script (see diff-highlight-screenshot.spec.ts).
export function syntaxHighlightingDisabledForAutomation(): boolean {
  if (typeof navigator === "undefined" || navigator.webdriver !== true) return false;
  return (globalThis as { __middlemanForceSyntaxHighlight?: boolean }).__middlemanForceSyntaxHighlight !== true;
}

export function getPierreDiffWorkerPool(): WorkerPoolManager | undefined {
  if (typeof Worker === "undefined") return undefined;
  if (syntaxHighlightingDisabledForAutomation()) return undefined;
  return getOrCreateWorkerPoolSingleton({
    poolOptions: {
      workerFactory: () =>
        new Worker(new URL("./pierre-diff-worker-entry.js", import.meta.url), {
          type: "module",
        }),
      poolSize: 4,
      totalASTLRUCacheSize: 200,
    },
    highlighterOptions: {
      theme: { dark: "pierre-dark", light: "pierre-light" },
      lineDiffType: "word",
      tokenizeMaxLineLength: diffTokenizeMaxLineLength,
      langs: ["bash", "css", "go", "html", "javascript", "json", "markdown", "sql", "toml", "typescript", "yaml"],
    },
  });
}
