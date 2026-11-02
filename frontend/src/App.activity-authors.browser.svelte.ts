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

it("keeps the author picker selectable across a live update without rereading candidates", async () => {
  await page.viewport(1280, 900);
  mounted = await mountBrowserApp("/?view=threaded", {
    overrides: [
      ({ url }) => (url.pathname === "/api/v1/activity/authors" ? jsonResponse({ authors: ["Alice", "Bob"] }) : null),
    ],
  });
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByRole("button", { name: "Filter authors: Anyone" }).click();
  await expect.element(page.getByRole("option", { name: "Alice", exact: true })).toBeVisible();

  let refreshRequests = 0;
  const fetch = globalThis.fetch;
  globalThis.fetch = (input, init) => {
    const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
    if (url.pathname === "/api/v1/activity/authors") {
      refreshRequests += 1;
      return Promise.resolve(jsonResponse({ authors: ["Alice", "Bob", "Carol"] }));
    }
    if (url.pathname === "/api/v1/activity") {
      // The live update brings a row whose parent author the picker lacks.
      // Candidates are read once per scope, so it must not reread them.
      return fetch(input, init).then(async (response) => {
        const body = (await response.json()) as Record<string, unknown>;
        const now = new Date().toISOString();
        const subject = {
          activity_at: now,
          item_number: 77,
          item_state: "open",
          item_title: "Carol's change",
          item_author: "Carol",
          item_type: "pr",
          platform_host: "github.com",
          repo_owner: "acme",
          repo_name: "widgets",
          repo: {
            provider: "github",
            platform_host: "github.com",
            platform_repo_id: 1001,
            owner: "acme",
            name: "widgets",
            repo_path: "acme/widgets",
          },
        };
        const item = {
          ...subject,
          id: "pr:77",
          cursor: "pr:77",
          activity_type: "new_pr",
          author: "Carol",
          created_at: now,
        };
        return jsonResponse({ ...body, items: [item], item_activity: [subject] });
      });
    }
    return fetch(input, init);
  };
  try {
    emitBrowserEventSource("data_changed", {});
    await expect.element(page.getByText("Carol's change")).toBeInTheDocument();
    flushSync();
    expect(refreshRequests).toBe(0);
    expect(document.querySelector("[role='listbox']")?.textContent).toContain("Alice");
    await page.getByRole("option", { name: "Alice", exact: true }).click();
    await vi.waitFor(() => expect(new URLSearchParams(window.location.search).get("author")).toBe("Alice"));
  } finally {
    globalThis.fetch = fetch;
  }
});
