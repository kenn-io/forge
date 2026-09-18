// Chromium-only profiling harnesses use tracing, CDP network emulation,
// and the workspace-switch:* User Timing measures. Run one spec at a
// time so each benchmark owns the browser and isolated fixture server.
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
