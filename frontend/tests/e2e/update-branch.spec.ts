import { expect, test } from "@playwright/test";
import { mockPullFixture } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

for (const width of [1440, 390]) {
  test(`updates a behind branch once and refreshes completion at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await mockApi(page);
    let head = "head-a";
    let updated = false;
    let calls = 0;
    const accepted = Promise.withResolvers<void>();
    const pull = mockPullFixture(42);
    const detail = () => ({
      merge_request: {
        ...pull,
        Author: "user-a",
        Title: "Update the feature branch",
        MergeableState: updated || width === 1440 ? "clean" : "behind",
        platform_head_sha: head,
      },
      repo: {
        ...pull.repo,
        platform_repo_id: 1001,
        capabilities: { ...pull.repo.capabilities, update_branch: true },
        operations: { update_branch: { available: !updated } },
      },
      repo_owner: "acme",
      repo_name: "widgets",
      platform_host: "github.com",
      platform_head_sha: head,
      reviewed_head_sha: head,
      detail_loaded: true,
      events: [],
    });
    await page.route("**/api/v1/pulls/github/acme/widgets/42**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith("/update-branch")) {
        calls++;
        expect(route.request().postDataJSON()).toEqual({ expected_head_sha: "head-a" });
        await accepted.promise;
        await route.fulfill({ status: 202, json: { status: "update_requested" } });
      } else if (path.endsWith("/42") || path.endsWith("/sync")) {
        if (route.request().method() === "POST") {
          head = "head-b";
          updated = true;
        }
        await route.fulfill({ json: detail() });
      } else await route.fallback();
    });
    await page.goto("/pulls/github/acme/widgets/42");
    const button = page.getByRole("button", { name: "Update branch", exact: true });
    if (width === 390) await page.getByRole("button", { name: "Actions", exact: true }).click();
    await button.click();
    if (width === 390) await page.getByRole("button", { name: "Actions", exact: true }).click();
    await expect(page.getByRole("button", { name: "Updating…", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "Updating…", exact: true }).evaluate((element) => {
      element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(calls).toBe(1);
    accepted.resolve();
    await expect(page.getByText("Branch changed. Pull request refreshed.")).toBeVisible();
    await expect(button).toHaveCount(0);
    expect(calls).toBe(1);
  });
}

for (const width of [1440, 390]) {
  test(`shows stale-head and uncertain outcomes at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await mockApi(page);
    const pull = mockPullFixture(42);
    let calls = 0;
    await page.route("**/api/v1/pulls/github/acme/widgets/42**", async (route) => {
      if (route.request().url().endsWith("/update-branch")) {
        calls++;
        await route.fulfill({
          status: calls === 1 ? 409 : 502,
          json: {
            code: calls === 1 ? "conflict" : "mutationOutcomeUnknown",
            status: calls === 1 ? 409 : 502,
            title: "Branch update failed",
            detail: calls === 1 ? "The head moved. Refresh this pull request." : "Check the provider before retrying.",
            details: { reason: "stale_state" },
          },
        });
      } else if (
        new URL(route.request().url()).pathname.endsWith("/42") ||
        new URL(route.request().url()).pathname.endsWith("/sync")
      ) {
        await route.fulfill({
          json: {
            merge_request: { ...pull, MergeableState: "behind" },
            repo: {
              ...pull.repo,
              platform_repo_id: 1001,
              capabilities: { ...pull.repo.capabilities, update_branch: true },
              operations: { update_branch: { available: true } },
            },
            repo_owner: "acme",
            repo_name: "widgets",
            platform_head_sha: "head-a",
            detail_loaded: true,
            events: [],
          },
        });
      } else await route.fallback();
    });
    await page.goto("/pulls/github/acme/widgets/42");
    if (width === 390) await page.getByRole("button", { name: "Actions", exact: true }).click();
    const button = page.getByRole("button", { name: "Update branch", exact: true });
    await button.click();
    await expect(page.getByText(/The head moved. Refresh this pull request/)).toBeVisible();
    await page.getByRole("button", { name: "Refresh detail", exact: true }).click();
    if (width === 390) await page.getByRole("button", { name: "Actions", exact: true }).click();
    await expect(button).toBeEnabled();
    await button.click();
    if (width === 390) await page.getByRole("button", { name: "Actions", exact: true }).click();
    await expect(page.getByRole("button", { name: "Check provider", exact: true })).toBeDisabled();
    expect(calls).toBe(2);
  });
}
