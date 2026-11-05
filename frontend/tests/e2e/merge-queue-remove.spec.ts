import { expect, test } from "@playwright/test";
import type { PullDetail } from "../../src/lib/api/types.js";
import { createMockApiHandler } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

test("the in-queue merge button removes the pull request from the merge queue after confirmation", async ({ page }) => {
  await mockApi(page);
  const api = createMockApiHandler();
  let queued = true;
  const dequeues: string[] = [];
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    const response = api.handle({ method: "GET", url: new URL(route.request().url()), bodyText: "" });
    const detail = (await response.json()) as PullDetail;
    detail.repo.capabilities.merge_mutation = true;
    detail.repo.capabilities.merge_queue_mutation = true;
    detail.merge_queue_target = { branch: "main", merge_method: "squash" };
    if (queued) {
      detail.merge_queue = {
        position: 3,
        state: "queued",
        enqueued_at: "2026-03-30T16:00:00Z",
        observed_at: "2026-03-30T16:00:00Z",
      };
    }
    await route.fulfill({ json: detail });
  });
  await page.route("**/api/v1/pulls/github/acme/widgets/42/merge/queue", async (route) => {
    dequeues.push(route.request().method());
    queued = false;
    await route.fulfill({ json: { status: "dequeued" } });
  });

  await page.goto("/pulls/github/acme/widgets/42");
  const button = page.locator(".btn--merge").first();
  await expect(button).toHaveText("In merge queue");
  await expect(button).toBeEnabled();

  await button.click();
  const confirm = page.getByRole("dialog", { name: "Remove from merge queue?" });
  await expect(confirm).toContainText("#42 will not merge until it is queued again.");
  await confirm.getByRole("button", { name: "Cancel" }).click();
  await expect(confirm).toHaveCount(0);
  expect(dequeues).toEqual([]);

  await button.click();
  await page.getByRole("dialog", { name: "Remove from merge queue?" }).getByRole("button", { name: "Remove" }).click();
  await expect(button).toHaveText("Add to merge queue");
  expect(dequeues).toEqual(["DELETE"]);
});
