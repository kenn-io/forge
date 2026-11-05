import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { createMockApiFetch, jsonResponse } from "./test/mockApiFetch.js";
import { INITIAL_RENDER_BUDGET } from "./lib/utils/render-budget.js";
import type { PullRequest } from "./lib/api/types.js";

let mounted: MountedBrowserApp | null = null;

beforeEach(async () => {
  localStorage.clear();
  await page.viewport(1280, 900);
});

afterEach(async () => {
  mounted?.unmount();
  mounted = null;
  localStorage.clear();
  await resetKeyboardModuleState();
});

function mountedRows(): number {
  return document.querySelectorAll(".pull-list .list-body .pull-item").length;
}

describe("PR sidebar label filter", () => {
  it("resets the mounted row budget when a label filter changes", async () => {
    const api = createMockApiFetch();
    const [base]: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
    const pulls = Array.from({ length: 400 }, (_, index) => ({
      ...base!,
      ID: 900 + index,
      Number: index + 1,
      Title: `Change ${index + 1}`,
      labels: [{ name: index % 2 === 0 ? "bug" : "docs", color: "d73a4a", is_default: false }],
    }));
    mounted = await mountBrowserApp("/pulls", {
      overrides: [(req) => (req.method === "GET" && req.url.pathname === "/api/v1/pulls" ? jsonResponse(pulls) : null)],
    });
    await vi.waitFor(() => expect(mountedRows()).toBe(INITIAL_RENDER_BUDGET));

    // Scrolling to the end grows the budget past the first screen of rows.
    await vi.waitFor(() => {
      document.querySelector(".pull-list .render-sentinel")?.scrollIntoView();
      expect(mountedRows()).toBeGreaterThan(INITIAL_RENDER_BUDGET);
    });
    // Back at the top, the end-of-list sentinel is out of reach and cannot grow the budget again.
    document.querySelector(".pull-list .pull-item")!.scrollIntoView();

    await page.elementLocator(document.querySelector(".pull-list .compact-filter-menu button")!).click();
    await page.getByRole("button", { name: /^Labels/ }).click();
    await page.getByRole("button", { name: /^bug/ }).click();

    await vi.waitFor(() => expect(mountedRows()).toBe(INITIAL_RENDER_BUDGET));
  });
});
