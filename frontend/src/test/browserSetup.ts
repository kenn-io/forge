import { afterAll } from "vite-plus/test";
import { cdp } from "vite-plus/test/browser";

// Chromium retains resources from completed Vitest iframes across files.
// Release them between files in this Chromium-only project.
// https://github.com/vitest-dev/vitest/issues/9437
afterAll(async () => {
  await cdp().send("HeapProfiler.collectGarbage");
});
