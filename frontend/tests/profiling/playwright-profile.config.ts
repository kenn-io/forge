// Playwright config for the workspace-switch profiling harness
// (`make profile-workspace-switch`). Chromium only: the harness uses
// browser.startTracing, which is a Chromium-specific API, to produce a
// chrome://tracing / Perfetto compatible trace that includes the
// workspace-switch:* User Timing measures.
import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  fullyParallel: false,
  workers: 1,
  // Profiling runs create real git worktrees and tmux sessions, then
  // measure repeated switches plus an optional ~30s Go trace window.
  timeout: 300_000,
  expect: { timeout: 15_000 },
  reporter: [["list"]],
  use: {
    ...devices["Desktop Chrome"],
    // Playwright's own tracing/screenshots would contaminate the
    // measured switches; the harness captures its own Chromium trace.
    trace: "off",
    screenshot: "off",
    video: "off",
  },
  projects: [{ name: "profile" }],
});
