// Regression guard for the embed-forced theme path. When an embed host pins
// window.__middleman_config.theme.mode, App.svelte resolves the theme from a
// $effect (reapplyTheme). A read-after-write on the forcedDark $state inside
// that effect makes it depend on a signal it mutates, so it reschedules until
// Svelte throws effect_update_depth_exceeded — the header still paints but the
// shell never advances past "Loading" and no list view mounts.
//
// The loop is emergent from the full App effect graph, not from reapplyTheme in
// isolation (an isolated $effect harness reads the settled value and never
// reschedules), so the store's unit tests cannot catch it. Mounting the real
// App with the mode forced is the smallest faithful reproduction: it asserts
// the shell renders past the loading state, which fails if the effect loops.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { cleanupTheme } from "./lib/stores/theme.svelte.js";

const WAIT = 10_000;

function count(selector: string): number {
  return document.querySelectorAll(selector).length;
}

describe("embed-forced theme mode", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 800);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    delete window.__middleman_config;
    // kit-ui-check-ignore: test harness resets the dark class between cases
    document.documentElement.classList.remove("dark");
    cleanupTheme();
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("renders the shell past loading when a mode is forced", async () => {
    // Set before mount: App reads the config during its initial render.
    window.__middleman_config = { theme: { mode: "dark" } };
    mounted = await mountBrowserApp("/pulls");

    // The list-view sidebar layout only mounts once the shell advances past
    // the loading state. If the theme reapply effect loops, the app stays on
    // "Loading" and this never appears.
    await vi.waitFor(() => expect(count(".kit-sidebar-layout__sidebar")).toBeGreaterThan(0), WAIT);

    // The forced mode actually took effect (so the guard exercises the forced
    // path, not the standalone one): dark class applied, toggle hidden.
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(count("button[title='Toggle theme']")).toBe(0);
  });
});
