import { expect, test } from "@playwright/test";
import type { PullDetail } from "../../src/lib/api/types.js";
import { createMockApiHandler } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

test("warns about failed CI before an explicit immediate merge", async ({ page }) => {
  await mockApi(page);
  const api = createMockApiHandler();
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    const response = api.handle({ method: "GET", url: new URL(route.request().url()), bodyText: "" });
    const detail = (await response.json()) as PullDetail;
    detail.merge_request.CIStatus = "failure";
    detail.merge_request.CIChecksJSON = JSON.stringify([
      { name: "build", status: "completed", conclusion: "failure", url: "", app: "CI" },
      { name: "integration", status: "in_progress", conclusion: "", url: "", app: "CI" },
    ]);
    await route.fulfill({ json: detail });
  });

  const mergeRequests: string[] = [];
  await page.route("**/api/v1/pulls/github/acme/widgets/42/merge**", async (route) => {
    mergeRequests.push(new URL(route.request().url()).pathname);
    await route.fulfill({ json: { merged: true, sha: "merge-sha", message: "merged" } });
  });

  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".btn--merge").first().click();
  const dialog = page.getByRole("dialog", { name: "Merge Pull Request" });
  await expect(dialog.getByRole("alert")).toHaveText(
    "CI has failed. Merging now will include changes with failing checks.",
  );
  await expect(dialog.getByRole("button", { name: "Merge after CI is complete" })).toHaveCount(0);
  expect(mergeRequests).toEqual([]);
  await dialog.getByRole("button", { name: "Merge Anyway" }).click();
  await expect(dialog).toHaveCount(0);
  expect(mergeRequests).toEqual(["/api/v1/pulls/github/acme/widgets/42/merge"]);
});
