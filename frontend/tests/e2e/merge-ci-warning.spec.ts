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
  await expect(dialog.getByRole("button", { name: "Merge after CI" })).toHaveCount(0);
  expect(mergeRequests).toEqual([]);
  await dialog.getByRole("button", { name: "Merge Anyway" }).click();
  await expect(dialog).toHaveCount(0);
  expect(mergeRequests).toEqual(["/api/v1/pulls/github/acme/widgets/42/merge"]);
});

test("the queued merge button cancels auto-merge while branch rules block merging", async ({ page }) => {
  await mockApi(page);
  const api = createMockApiHandler();
  let queued = true;
  const cancellations: string[] = [];
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    const response = api.handle({ method: "GET", url: new URL(route.request().url()), bodyText: "" });
    const detail = (await response.json()) as PullDetail;
    detail.deferred_merge_pending = queued;
    detail.merge_request.MergeableState = "blocked";
    detail.merge_request.merge_rules = "blocked";
    detail.merge_request.CIStatus = "pending";
    await route.fulfill({ json: detail });
  });
  await page.route("**/api/v1/pulls/github/acme/widgets/42/merge/deferred", async (route) => {
    cancellations.push(route.request().method());
    queued = route.request().method() === "POST";
    await route.fulfill({ json: { status: queued ? "queued" : "cancelled" } });
  });

  await page.goto("/pulls/github/acme/widgets/42");
  const button = page.locator(".btn--merge").first();
  await expect(button).toHaveText("Merging after CI");
  await expect(button).toBeEnabled();
  await button.click();
  await expect(button).toHaveText("Merge after CI");
  expect(cancellations).toEqual(["DELETE"]);
  await expect(page.getByRole("dialog", { name: "Merge Pull Request" })).toHaveCount(0);
  await button.click();
  const dialog = page.getByRole("dialog", { name: "Merge Pull Request" });
  await dialog.getByRole("button", { name: "Merge after CI", exact: true }).click();
  await expect(button).toHaveText("Merging after CI");
  expect(cancellations).toEqual(["DELETE", "POST"]);
});
