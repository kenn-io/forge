import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer } from "./support/e2eServer";

test.describe("PR detail branch info", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/1");
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });
  });

  test("shows head and base branch buttons", async ({ page }) => {
    const metaBranch = page.locator(".meta-branch");
    await expect(metaBranch).toBeVisible();

    const branchBtns = metaBranch.locator(".branch-name-btn");
    await expect(branchBtns).toHaveCount(2);
    await expect(branchBtns.first()).not.toBeEmpty();
    await expect(branchBtns.last()).not.toBeEmpty();

    const arrow = metaBranch.locator(".branch-arrow");
    await expect(arrow).toBeVisible();
  });

  test("click branch shows copied feedback", async ({ page, context, browserName }) => {
    if (browserName === "chromium") {
      await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    }

    const headBtn = page.locator(".meta-branch .branch-name-btn").first();
    await expect(headBtn).toHaveAttribute("title", "Click to copy");

    await headBtn.click();

    await expect
      .poll(async () =>
        headBtn.evaluate(
          (element) =>
            element.classList.contains("branch-name-btn--copied") && element.getAttribute("title") === "Copied!",
        ),
      )
      .toBe(true);
  });

  test("reveals current approvers from full-stack review events", async ({ page }) => {
    const trigger = page.getByRole("button", { name: "APPROVED (2)" });
    await expect(trigger).toBeVisible();

    await trigger.click();

    const popup = page.locator(".approval-popup");
    await expect(popup).toContainText("alice");
    await expect(popup).toContainText("bob");
    await expect(popup).not.toContainText("carol");
  });

  test("summarizes changed lines by category in the popover", async ({ page }) => {
    await page.route("**/api/v1/pulls/github/acme/widgets/1/files", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          stale: false,
          files: [
            {
              path: "docs/review-plan.md",
              old_path: "docs/review-plan.md",
              status: "modified",
              is_binary: false,
              is_whitespace_only: false,
              additions: 10,
              deletions: 2,
              hunks: [],
            },
            {
              path: "internal/server/api.go",
              old_path: "internal/server/api.go",
              status: "modified",
              is_binary: false,
              is_whitespace_only: false,
              additions: 180,
              deletions: 20,
              hunks: [],
            },
            {
              path: "internal/server/api_test.go",
              old_path: "internal/server/api_test.go",
              status: "modified",
              is_binary: false,
              is_whitespace_only: false,
              additions: 49,
              deletions: 7,
              hunks: [],
            },
          ],
        }),
      });
    });

    await page.goto("/pulls/github/acme/widgets/1");
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    const trigger = page.locator(".diff-summary-trigger");
    await expect(trigger).toHaveText(/\+240\s+−30/);

    await trigger.focus();

    const popover = page.locator(".diff-summary-popover");
    await expect(popover).toBeVisible();
    await expect(popover).toContainText(/Plans\/docs\s+\+10\s+−2[\s\S]*Code\s+\+180\s+−20[\s\S]*Tests\s+\+49\s+−7/);
    await expect(popover).not.toContainText("Other");
  });
});

test("diff summary uses real files after the PR head advances", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    await page.addInitScript(() => {
      const realSetInterval = window.setInterval;
      window.setInterval = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) =>
        realSetInterval(handler, timeout === 60_000 ? 100 : timeout, ...args)) as typeof window.setInterval;
    });
    await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.locator(".sync-indicator")).toHaveCount(0);

    const trigger = page.locator(".diff-summary-trigger");
    await expect(trigger).toHaveText(/\+240\s+−30/);
    await trigger.focus();

    const popover = page.locator(".diff-summary-popover");
    await expect(popover).toBeVisible();
    await expect(popover).toContainText(/Code\s+\+\d+\s+−\d+/);
    await expect(popover).not.toContainText("Tests");
    const initialSummary = await popover.textContent();

    const response = await page.request.post(`${server.info.base_url}/__e2e/pr-diff-summary/advance-head`);
    expect(response.ok()).toBe(true);
    const advanced = (await response.json()) as { head_sha: string };

    await expect(trigger).toHaveAttribute("aria-describedby", new RegExp(advanced.head_sha.slice(0, 10)));
    await expect(popover).toContainText(/Plans\/docs\s+\+\d+\s+−\d+/);
    await expect(popover).toContainText(/Tests\s+\+\d+\s+−\d+/);
    await expect(popover).not.toHaveText(initialSummary ?? "");
  } finally {
    await server.stop();
  }
});
