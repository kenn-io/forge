// Converted from frontend/tests/e2e-full/app-startup.spec.ts.
//
// runAppStartup's race against SETTINGS_STARTUP_TIMEOUT_MS is unit-tested
// in src/lib/utils/appStartup.test.ts. This test covers the App-level
// wiring the e2e proved: App.svelte hands the real getSettings to the
// REAL runAppStartup (the appShellInstantStartupMock preset is NOT
// imported here, unlike the other App.*.test.ts suites), shows its
// loading state while settings stall, and once the 8s timeout fires it
// flips appReady, renders the default view, and the post-onReady loaders
// on the provider stores run.
//
// Note vs the e2e spec: the spec asserted `pulls || issues || activity`
// network requests. In code, runAppStartup's post-onReady block calls
// loadPulls/loadIssues/startPolling/events.connect; loadActivity is
// triggered by the repo-change $effect and by the activity view itself
// (stubbed here), so this test asserts the loaders startup actually owns.
import { cleanup, render } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import "./lib/testing/appShellMocks.js";
import { createAppTarget, installBrowserGlobals } from "./lib/testing/appShellMocks.js";
import { providerCalls, resetProviderCalls } from "./lib/testing/appProviderConfiguredStores.js";

const settingsApi = vi.hoisted(() => ({
  calls: 0,
}));

vi.mock("./lib/api/settings.js", () => ({
  // Stall settings indefinitely, like the e2e page.route that never
  // fulfilled /api/v1/settings. The startup helper must time out.
  getSettings: () => {
    settingsApi.calls += 1;
    return new Promise(() => {});
  },
}));

vi.mock("@middleman/ui", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@middleman/ui")>();
  const Provider = (await import("./lib/testing/AppProviderConfiguredMock.svelte")).default;
  const { configuredMockStores } = await import("./lib/testing/appProviderConfiguredStores.js");
  const Stub = (await import("./lib/testing/AppViewStub.svelte")).default;
  return {
    ...actual,
    Provider,
    getStores: () => configuredMockStores,
    PRListView: Stub,
    IssueListView: Stub,
    ActivityFeedView: Stub,
    MobileActivityView: Stub,
    KanbanBoardView: Stub,
    ReviewsView: Stub,
    FocusListView: Stub,
  };
});

vi.mock("./lib/components/layout/AppHeader.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));

function mainLoadingState(): Element | null {
  return document.querySelector(".app-main .loading-state");
}

function mainViewStub(): Element | null {
  return document.querySelector(".app-main [data-testid='app-view-stub']");
}

// waitFor is unusable here: vitest fake timers also fake the intervals
// waitFor polls with. Effects and microtasks are flushed explicitly via
// tick() and advanceTimersByTimeAsync instead.
async function flushRender(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await tick();
  }
}

describe("app startup settings timeout", () => {
  beforeEach(async () => {
    installBrowserGlobals();
    vi.useFakeTimers();
    settingsApi.calls = 0;
    resetProviderCalls();
    window.history.replaceState(null, "", "/");
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/");
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    for (const el of Array.from(document.querySelectorAll("#app"))) {
      el.remove();
    }
    vi.unstubAllGlobals();
  });

  it("recovers and loads data when settings stall past the startup timeout", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await flushRender();

    // The real runAppStartup received App's real getSettings dependency
    // and is now stalled on it: loading state, no default view, no
    // post-onReady loader activity yet.
    expect(settingsApi.calls).toBe(1);
    expect(mainLoadingState()?.textContent).toContain("Loading");
    expect(mainViewStub()).toBeNull();
    expect(providerCalls.loadPulls).toBe(0);
    expect(providerCalls.loadIssues).toBe(0);
    expect(providerCalls.startPolling).toBe(0);
    expect(providerCalls.eventsConnect).toBe(0);

    // Just shy of the 8s SETTINGS_STARTUP_TIMEOUT_MS nothing changes.
    await vi.advanceTimersByTimeAsync(7_900);
    await flushRender();
    expect(mainLoadingState()).not.toBeNull();
    expect(providerCalls.loadPulls).toBe(0);

    // Cross the timeout: the race rejects, the catch falls back to
    // defaults, onReady flips appReady, and startup keeps going.
    await vi.advanceTimersByTimeAsync(200);
    await flushRender();

    expect(warn).toHaveBeenCalledWith(
      "Failed to load settings, using defaults:",
      expect.objectContaining({
        message: "timed out loading settings during startup",
      }),
    );
    expect(mainLoadingState()).toBeNull();
    // Default route "/" resolves to the activity view (stubbed), which
    // only mounts once appReady is true.
    expect(mainViewStub()).not.toBeNull();
    expect(providerCalls.loadPulls).toBe(1);
    expect(providerCalls.loadIssues).toBe(1);
    expect(providerCalls.startPolling).toBe(1);
    expect(providerCalls.eventsConnect).toBe(1);
    // The stalled settings request was made exactly once; the timeout
    // recovery does not refetch.
    expect(settingsApi.calls).toBe(1);

    warn.mockRestore();
  });
});
