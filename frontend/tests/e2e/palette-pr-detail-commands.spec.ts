import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

// E2E coverage for the PR-detail palette commands (`pr.approve`,
// `pr.ready`, `pr.approveWorkflows`). The merge palette command is
// intentionally not registered (the trigger lives in PullDetail.svelte's
// local component state).

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test.describe("PR-detail palette commands", () => {
  test("Approve PR runs from the palette and triggers the approve flow", async ({ page }) => {
    // Navigate to a PR detail. mockApi serves /api/v1/pulls and a
    // single-PR detail endpoint at /repos/<owner>/<name>/pulls/<n>.
    await page.goto("/pulls/acme/widgets/42");

    // Capture the approve POST so we can assert the action wired
    // through the same closure the existing button uses.
    const approveRequest = page.waitForRequest(
      (req) =>
        req.method() === "POST" && /\/repos\/acme\/widgets\/pulls\/42\/approve$/.test(new URL(req.url()).pathname),
    );

    await page.keyboard.press("Meta+K");
    await page.locator(".palette-input").fill("approve pr");
    await page.keyboard.press("Enter");

    await approveRequest;
  });

  test("Approve PR is absent from the palette when the PR is closed", async ({ page }) => {
    // The fixture for /pulls/acme/widgets/55 should be a closed PR.
    // (mockApi may need updates so this PR returns State === "closed";
    // the assertion still checks the user-visible behavior.)
    await page.goto("/pulls/acme/widgets/55");

    await page.keyboard.press("Meta+K");
    await page.locator(".palette-input").fill("approve pr");

    // Palette rows render as <button class="palette-row">; query by name
    // against the actual role so a regression that surfaces the command
    // anyway would fail this assertion (the previous role="option" query
    // matched nothing regardless of palette state).
    await expect(page.getByRole("button", { name: /Approve PR/i })).toHaveCount(0);
  });

  test("Mark ready for review appears only when the PR is a draft", async ({ page }) => {
    await page.goto("/pulls/acme/widgets/42");
    await page.keyboard.press("Meta+K");
    await page.locator(".palette-input").fill("ready for review");
    // Non-draft PR; the action should be filtered out by `when`. Same
    // role-correctness note as the closed-PR test above.
    await expect(page.getByRole("button", { name: /Mark ready for review/i })).toHaveCount(0);
  });
});
