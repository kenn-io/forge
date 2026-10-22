import { expect, test } from "@playwright/test";
import type { PullDetail } from "../../src/lib/api/types.js";
import { createMockApiHandler } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

test("explains that a draft must be marked ready instead of attempting the merge", async ({ page }) => {
  await mockApi(page);
  const api = createMockApiHandler();
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    const response = api.handle({ method: "GET", url: new URL(route.request().url()), bodyText: "" });
    const detail = (await response.json()) as PullDetail;
    detail.merge_request.IsDraft = true;
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
    "This pull request is a draft. Mark it ready for review before merging.",
  );
  await expect(dialog.locator(".kit-modal-footer button.btn--primary")).toBeDisabled();
  expect(mergeRequests).toEqual([]);
});
