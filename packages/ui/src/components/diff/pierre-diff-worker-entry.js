// Local worker entry wrapping the @pierre/diffs worker package.
// Bundlers (Vite/rolldown worker-import-meta-url) resolve relative
// worker URLs but not bare package specifiers inside
// `new URL(..., import.meta.url)`, so consumers that link this package
// from another Vite project fail to bundle the worker without this
// indirection.
//
// @pierre/diffs does not mark its worker entry as side-effectful, so
// a bare import can be tree-shaken into an empty worker asset. Keep the
// namespace reachable so the import cannot be erased.
import * as workerModule from "@pierre/diffs/worker/worker.js";

globalThis.__middlemanPierreDiffWorkerModule = workerModule;
