import type { Page } from "@playwright/test";

// Serves the first `limit` rows for Closed and All and an empty Open tab.
// Returns the limit of each Closed request.
export async function routeClosedPaging(page: Page, endpoint: string, rows: readonly unknown[]): Promise<number[]> {
  const closedLimits: number[] = [];
  await page.route(`**${endpoint}**`, async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || url.pathname !== endpoint) {
      await route.fallback();
      return;
    }
    const limit = Number(url.searchParams.get("limit")) || rows.length;
    const state = url.searchParams.get("state");
    if (state === "closed") closedLimits.push(limit);
    await route.fulfill({ json: state === "open" ? [] : rows.slice(0, limit) });
  });
  return closedLimits;
}
