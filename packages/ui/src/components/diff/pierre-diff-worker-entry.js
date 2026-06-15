// Local worker entry wrapping the @pierre/diffs worker package.
// Bundlers (Vite/rolldown worker-import-meta-url) resolve relative
// worker URLs but not bare package specifiers inside
// `new URL(..., import.meta.url)`, so consumers that link this package
// from another Vite project fail to bundle the worker without this
// indirection.
import "@pierre/diffs/worker/worker.js";
