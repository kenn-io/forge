import { expect, type Page, test } from "@playwright/test";
import { mockPullFixture } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

function pullDetail(head: string, mergeableState: string, available: boolean) {
  const pull = mockPullFixture(42);
  return {
    merge_request: { ...pull, MergeableState: mergeableState, platform_head_sha: head },
    repo: {
      ...pull.repo,
      platform_repo_id: 1001,
      capabilities: { ...pull.repo.capabilities, update_branch: true },
      operations: { update_branch: { available } },
    },
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    platform_head_sha: head,
    reviewed_head_sha: head,
    detail_loaded: true,
    events: [],
  };
}

for (const width of [1440, 390]) {
  const toggleActionsMenu = async (page: Page) => {
    if (width === 390) await page.getByRole("button", { name: "Actions", exact: true }).click();
  };

  test(`updates a behind branch once and refreshes completion at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await mockApi(page);
    let head = "head-a";
    let updated = false;
    let calls = 0;
    const accepted = Promise.withResolvers<void>();
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
        // Desktop covers a clean branch that is still behind its base.
        await route.fulfill({ json: pullDetail(head, updated || width === 1440 ? "clean" : "behind", !updated) });
      } else await route.fallback();
    });
    await page.goto("/pulls/github/acme/widgets/42");
    const button = page.getByRole("button", { name: "Update branch", exact: true });
    await toggleActionsMenu(page);
    await button.click();
    await toggleActionsMenu(page);
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

  test(`shows stale-head and uncertain outcomes at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await mockApi(page);
    let calls = 0;
    await page.route("**/api/v1/pulls/github/acme/widgets/42**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith("/update-branch")) {
        calls++;
        const stale = calls === 1;
        await route.fulfill({
          status: stale ? 409 : 502,
          json: {
            code: stale ? "conflict" : "mutationOutcomeUnknown",
            status: stale ? 409 : 502,
            title: "Branch update failed",
            detail: stale ? "The head moved. Refresh this pull request." : "Check the provider before retrying.",
            details: { reason: "stale_state" },
          },
        });
      } else if (path.endsWith("/42") || path.endsWith("/sync")) {
        await route.fulfill({ json: pullDetail("head-a", "behind", true) });
      } else await route.fallback();
    });
    await page.goto("/pulls/github/acme/widgets/42");
    await toggleActionsMenu(page);
    const button = page.getByRole("button", { name: "Update branch", exact: true });
    await button.click();
    await expect(page.getByText(/The head moved. Refresh this pull request/)).toBeVisible();
    await page.getByRole("button", { name: "Refresh detail", exact: true }).click();
    await toggleActionsMenu(page);
    await expect(button).toBeEnabled();
    await button.click();
    await toggleActionsMenu(page);
    await expect(page.getByRole("button", { name: "Check provider", exact: true })).toBeDisabled();
    expect(calls).toBe(2);
  });
}
