// Browser-tier analog of App.teardown.test.ts.
//
// Unmounting the app shell must interrupt its Effect-owned sync polling. A
// leaked polling fiber keeps firing across tests against whichever fetch stub
// is current (and keeps workers alive).
//
// Browser translation: the real Chromium page provides matchMedia /
// ResizeObserver / IntersectionObserver / canvas natively, so installAppDomGlobals()
// is gone; mountBrowserApp stubs only EventSource.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse } from "./test/mockApiFetch.js";

describe("app shell teardown", () => {
  vi.setConfig({ testTimeout: 20_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
    mounted = null;
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("stops sync status requests when the application shell unmounts", async () => {
    const app = await mountBrowserApp("/pulls", {
      overrides: [
        (request) =>
          request.method === "GET" && request.url.pathname === "/api/v1/sync/status"
            ? jsonResponse({ running: true, progress: "acme/widgets", last_run_at: "", last_error: "" })
            : null,
      ],
    });
    mounted = app;
    await vi.waitFor(() => {
      expect(app.api.requests.filter((request) => request.url.pathname === "/api/v1/sync/status")).toHaveLength(1);
    });
    await vi.waitFor(() => expect(document.querySelector('[aria-label="Syncing"]')).not.toBeNull());

    app.unmount();
    mounted = null;
    const requestsAfterUnmount = app.api.requests.filter(
      (request) => request.url.pathname === "/api/v1/sync/status",
    ).length;
    await new Promise((resolve) => setTimeout(resolve, 2_500));

    expect(app.api.requests.filter((request) => request.url.pathname === "/api/v1/sync/status")).toHaveLength(
      requestsAfterUnmount,
    );
  });

  it("owns Roborev health polling across an embedded workspace route", async () => {
    const app = await mountBrowserApp("/workspaces/embed/detail/github/pr/github.com/1?repo_path=acme%2Fwidgets", {
      overrides: [
        (request) =>
          request.method === "GET" && request.url.pathname === "/api/v1/roborev/status"
            ? jsonResponse({ available: false, endpoint: "", version: "" })
            : null,
      ],
    });
    mounted = app;

    await vi.waitFor(() => {
      expect(app.api.requests.filter((request) => request.url.pathname === "/api/v1/roborev/status")).toHaveLength(1);
    });

    app.unmount();
    mounted = null;
    const requestsAfterUnmount = app.api.requests.filter(
      (request) => request.url.pathname === "/api/v1/roborev/status",
    ).length;
    await new Promise((resolve) => setTimeout(resolve, 1_500));

    expect(app.api.requests.filter((request) => request.url.pathname === "/api/v1/roborev/status")).toHaveLength(
      requestsAfterUnmount,
    );
  });
});
