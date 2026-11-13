import type { Page } from "@playwright/test";

// Serves the first `limit` rows for Closed and All and an empty Open tab.
// `matches` applies the request's filters before the limit, as the server does.
// Returns the limit of each Closed request.
export async function routeClosedPaging<Row>(
  page: Page,
  endpoint: string,
  rows: readonly Row[],
  matches: (row: Row, params: URLSearchParams) => boolean = () => true,
): Promise<number[]> {
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
    const matching = rows.filter((row) => matches(row, url.searchParams));
    await route.fulfill({ json: state === "open" ? [] : matching.slice(0, limit) });
  });
  return closedLimits;
}
