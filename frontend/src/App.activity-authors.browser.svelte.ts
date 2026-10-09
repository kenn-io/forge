import { afterEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { flushSync } from "svelte";
import {
  emitBrowserEventSource,
  mountBrowserApp,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { jsonResponse } from "./test/mockApiFetch.js";

let mounted: MountedBrowserApp | undefined;

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  localStorage.clear();
  sessionStorage.clear();
  await resetKeyboardModuleState();
});

it("keeps the author picker selectable while live updates refresh candidates", async () => {
  await page.viewport(1280, 900);
  mounted = await mountBrowserApp("/?view=threaded", {
    overrides: [
      ({ url }) => (url.pathname === "/api/v1/activity/authors" ? jsonResponse({ authors: ["Alice", "Bob"] }) : null),
    ],
  });
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByRole("button", { name: "Filter authors: Anyone" }).click();
  await expect.element(page.getByRole("option", { name: "Alice", exact: true })).toBeVisible();

  const pending = Promise.withResolvers<Response>();
  let refreshRequests = 0;
  const fetch = globalThis.fetch;
  globalThis.fetch = (input, init) => {
    const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
    if (url.pathname === "/api/v1/activity/authors") {
      refreshRequests += 1;
      return pending.promise.then((response) => response.clone());
    }
    return fetch(input, init);
  };
  try {
    emitBrowserEventSource("data_changed", {});
    await vi.waitFor(() => expect(refreshRequests).toBeGreaterThan(0));
    flushSync();
    expect(document.querySelector("[role='listbox']")?.textContent).toContain("Alice");
    await page.getByRole("option", { name: "Alice", exact: true }).click();
    await vi.waitFor(() => expect(new URLSearchParams(window.location.search).get("author")).toBe("Alice"));
  } finally {
    pending.resolve(jsonResponse({ authors: ["Alice", "Bob", "Carol"] }));
    globalThis.fetch = fetch;
  }
});
