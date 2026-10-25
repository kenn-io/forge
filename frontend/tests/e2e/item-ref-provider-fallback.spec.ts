import { expect, test } from "@playwright/test";
import type { PullDetail } from "../../src/lib/api/types.js";
import { createMockApiHandler } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

const providerURL = "https://github.com/acme/widgets/pull/999";

test("opens the provider page for an item Forge has not synced", async ({ page }) => {
  await mockApi(page);
  const api = createMockApiHandler();
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    const response = api.handle({ method: "GET", url: new URL(route.request().url()), bodyText: "" });
    const detail = (await response.json()) as PullDetail;
    detail.merge_request.Body = `Follows up on ${providerURL}.`;
    await route.fulfill({ json: detail });
  });
  await page.route("**/api/v1/repo/github/acme/widgets/resolve/999**", (route) =>
    route.fulfill({
      status: 404,
      contentType: "application/problem+json",
      json: { status: 404, code: "notFound", title: "Not Found", detail: "item not found" },
    }),
  );
  await page.route(providerURL, (route) =>
    route.fulfill({ contentType: "text/html", body: "<title>Provider pull request</title>" }),
  );

  await page.goto("/pulls/github/acme/widgets/42");
  const link = page.locator(`a.item-ref[data-external-url="${providerURL}"]`).first();
  await expect(link).toBeVisible();
  await link.click();

  await expect(page).toHaveURL(providerURL);
  await expect(page).toHaveTitle("Provider pull request");
});
