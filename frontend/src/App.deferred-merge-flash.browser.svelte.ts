// A failed deferred merge reports through a flash raised while the user may be
// on another page; the flash opens the failed pull request inside Forge.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  emitBrowserEventSource,
  getBrowserEventSourceCount,
  mountBrowserApp,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const WAIT = 10_000;

// The failure handler reconciles the PR list and the Activity feed before it
// reports, so both reads must succeed.
const activityOverrides: MockRouteOverride[] = [
  (req) =>
    req.method === "GET" && req.url.pathname === "/api/v1/activity" ? jsonResponse({ capped: false, items: [] }) : null,
  (req) =>
    req.method === "GET" && req.url.pathname === "/api/v1/activity/authors" ? jsonResponse({ authors: [] }) : null,
];

describe("deferred merge failure flash", () => {
  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    const { dismissFlash } = await import("./lib/stores/flash.svelte.js");
    dismissFlash();
  });

  afterEach(async () => {
    const { dismissFlash } = await import("./lib/stores/flash.svelte.js");
    dismissFlash();
    mounted?.unmount();
    mounted = null;
  });

  it("opens the failed pull request on its own provider and host", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/pulls", { overrides: activityOverrides });
    await vi.waitFor(() => expect(document.querySelector(".kit-sidebar-layout__sidebar")).not.toBeNull(), WAIT);
    await vi.waitFor(() => expect(getBrowserEventSourceCount()).toBeGreaterThan(0), WAIT);

    emitBrowserEventSource("deferred_merge_completed", {
      provider: "gitlab",
      platform_host: "gitlab.example.test",
      repo_path: "acme/platform/widget",
      owner: "acme/platform",
      name: "widget",
      number: 7,
      head_sha: "2222222",
      status: "failed",
      error: "merge queue rejected the change",
      completed_at: "2026-07-10T15:00:00Z",
    });

    const banner = await vi.waitFor(() => {
      const element = document.querySelector<HTMLElement>(".kit-flash-banner");
      expect(element?.textContent).toContain(
        "Deferred merge for acme/platform/widget#7 failed: merge queue rejected the change",
      );
      return element!;
    }, WAIT);
    await page.elementLocator(banner).getByRole("button", { name: "Open pull request" }).click();

    expect(window.location.pathname).toBe("/host/gitlab.example.test/pulls/gitlab/acme%2Fplatform/widget/7");
    expect(document.querySelector(".kit-flash-banner")).toBeNull();
  });
});
