import { expect, test, type Page } from "@playwright/test";
import type { PullRequest, Issue } from "../../src/lib/api/types.js";
import { createMockApiHandler } from "../../src/test/mockApiFetch.js";
import { mockApi, mockSettings } from "./support/mockApi";

async function expectAgentAtRight(page: Page): Promise<void> {
  const gap = await page
    .locator(".cell--title .agent-state")
    .first()
    .evaluate((label) => {
      const cell = label.parentElement!;
      return cell.getBoundingClientRect().right - label.getBoundingClientRect().right;
    });
  expect(gap).toBeLessThanOrEqual(1);
}

async function mockAgentStatus(page: Page, enabled = false) {
  await mockApi(page);
  const state = { settings: structuredClone(mockSettings), pullAgentState: "working" };
  state.settings.workspaces.show_agent_status_in_lists = enabled;
  await page.route("**/api/v1/settings", async (route) => {
    if (route.request().method() === "PUT") {
      const update = route.request().postDataJSON();
      expect(update).toEqual({ workspaces: { show_agent_status_in_lists: true } });
      state.settings = { ...state.settings, workspaces: { ...state.settings.workspaces, ...update.workspaces } };
    }
    await route.fulfill({ json: state.settings });
  });
  const api = createMockApiHandler();
  const pulls: PullRequest[] = await api
    .handle({ method: "GET", url: new URL("http://localhost/api/v1/pulls"), bodyText: "" })
    .json();
  const issues: Issue[] = await api
    .handle({ method: "GET", url: new URL("http://localhost/api/v1/issues"), bodyText: "" })
    .json();
  const pull = pulls[0]!;
  const issue = issues[0]!;
  await page.route("**/api/v1/pulls?*", (route) =>
    route.fulfill({
      json: pulls.map((item) => ({
        ...item,
        workspace: { id: `pr-${item.Number}`, status: "ready", agent_state: state.pullAgentState },
      })),
    }),
  );
  await page.route("**/api/v1/issues?*", (route) =>
    route.fulfill({
      json: issues.map((item) => ({
        ...item,
        workspace: { id: `issue-${item.Number}`, status: "ready", agent_state: "approval" },
      })),
    }),
  );
  await page.route("**/api/v1/activity?*", (route) =>
    route.fulfill({
      json: {
        capped: false,
        items: [
          {
            id: "agent-status-event",
            cursor: "agent-status-event",
            activity_type: "comment",
            author: "reviewer",
            body_preview: "Ready for review",
            created_at: new Date().toISOString(),
            item_number: pull.Number,
            item_state: "open",
            item_title: pull.Title,
            item_type: "pr",
            item_url: pull.URL,
            platform_host: pull.platform_host,
            repo_owner: pull.repo_owner,
            repo_name: pull.repo_name,
            repo: pull.repo,
            workspace: { id: `pr-${pull.Number}`, status: "ready", agent_state: "done" },
          },
        ],
      },
    }),
  );

  return { state, pull, issue };
}

test("Forge config preference enables linked pull agent states without changing row layout", async ({ page }) => {
  test.setTimeout(60_000);
  const { state, pull } = await mockAgentStatus(page);

  await page.goto("/pulls");
  await expect(page.locator(".pr-list-row").filter({ hasText: pull.Title })).toBeVisible();
  await expect(page.locator(".pr-list-row .agent-state")).toHaveCount(0);
  const originalHeight = await page
    .locator(".pr-list-row")
    .filter({ hasText: pull.Title })
    .evaluate((row) => row.getBoundingClientRect().height);
  await page.locator('button[title="Settings"]').click();
  await page
    .getByRole("navigation", { name: "Settings" })
    .getByRole("button", { name: /^Workspaces / })
    .click();
  const toggle = page.getByRole("button", { name: "Show agent status in lists" });
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => state.settings.workspaces.show_agent_status_in_lists).toBe(true);
  await page.evaluate(() => localStorage.clear());
  await page.goto("/pulls");
  await expect(
    page.locator(".pr-list-row").filter({ hasText: pull.Title }).getByText("Working", { exact: true }),
  ).toBeVisible();
  expect(
    await page
      .locator(".pr-list-row")
      .filter({ hasText: pull.Title })
      .evaluate((row) => row.getBoundingClientRect().height),
  ).toBe(originalHeight);
  const numberGaps = await page.locator(".pr-list-row").evaluateAll((rows) =>
    rows.map((row) => {
      const agent = row.querySelector(".agent-state")!.getBoundingClientRect();
      const number = row.querySelector(".item-number")!.getBoundingClientRect();
      return {
        horizontal: number.left - agent.right,
        vertical: Math.abs((number.top + number.bottom) / 2 - (agent.top + agent.bottom) / 2),
      };
    }),
  );
  for (const gap of numberGaps) {
    expect(gap.horizontal).toBeLessThan(12);
    expect(gap.vertical).toBeLessThan(1);
  }
  await page.screenshot({ path: test.info().outputPath("pull-list-agent-status.png") });
  state.pullAgentState = "input";
  await expect(
    page.locator(".pr-list-row").filter({ hasText: pull.Title }).getByText("Input", { exact: true }),
  ).toBeVisible({ timeout: 12_000 });
});

test("shows linked issue agent states when enabled in Forge config", async ({ page }) => {
  const { issue } = await mockAgentStatus(page, true);
  await page.goto("/issues");
  await expect(
    page.locator(".issue-item").filter({ hasText: issue.Title }).getByText("Approval", { exact: true }),
  ).toBeVisible();
});

for (const view of ["flat", "threaded"] as const) {
  test(`shows linked agent states at the right of ${view} Activity rows`, async ({ page }) => {
    await mockAgentStatus(page, true);
    await page.goto(`/?view=${view}`);
    await expect(page.locator(".agent-state").getByText("Done", { exact: true }).first()).toBeVisible();
    await expectAgentAtRight(page);
    await page.screenshot({ path: test.info().outputPath(`list-agent-status-${view}.png`) });
  });
}
