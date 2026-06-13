// Playwright page.route adapter over the shared mock API in
// src/test/mockApiFetch.ts. The fixtures and /api/v1 route matching live
// there (one definition for both the Playwright and Vitest suites); this
// file only translates Playwright's Route/Request into the shared handler's
// request shape. Specs layer scenario-specific page.route overrides on top
// as before — later registrations win, so they shadow these defaults.

import type { Page } from "@playwright/test";

import { createMockApiHandler } from "../../../src/test/mockApiFetch";

export async function mockApi(page: Page): Promise<void> {
  const api = createMockApiHandler();

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());

    if (request.method() === "GET" && url.pathname === "/api/v1/events") {
      // Server-Sent Events endpoint. Tests don't need live data over the
      // wire, so reply with an empty SSE stream that keeps the connection
      // open just long enough for the page to settle. EventSource fires an
      // `error` event when the response closes; the events store treats
      // that as "disconnected" and the rest of the app keeps working.
      // (The jsdom suite stubs EventSource instead, so this stays here.)
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ":\n\n",
      });
      return;
    }

    const response = api.handle({
      method: request.method().toUpperCase(),
      url,
      bodyText: request.postData() ?? "",
    });
    await route.fulfill({
      status: response.status,
      contentType: response.headers.get("Content-Type") ?? "application/json",
      body: await response.text(),
    });
  });
}
