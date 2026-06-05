import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer } from "./support/e2eServer";

type BrowserName = "chromium" | "firefox" | "webkit";

type StatusTarget = {
  owner: string;
  repo: string;
  number: number;
  status: string;
  label: string;
};

const targets: Record<BrowserName, StatusTarget> = {
  chromium: {
    owner: "acme",
    repo: "widgets",
    number: 1,
    status: "waiting",
    label: "Waiting",
  },
  firefox: {
    owner: "acme",
    repo: "widgets",
    number: 2,
    status: "awaiting_merge",
    label: "Awaiting Merge",
  },
  webkit: {
    owner: "acme",
    repo: "widgets",
    number: 6,
    status: "reviewing",
    label: "Reviewing",
  },
};

test("workflow status dropdown persists through API and database", async ({
  page,
  browserName,
}) => {
  const target = targets[browserName as BrowserName] ?? targets.chromium;
  const server = await startIsolatedE2EServer();

  try {
    const detailPath = `/pulls/github/${target.owner}/${target.repo}/${target.number}`;
    await page.goto(`${server.info.base_url}${detailPath}`);
    await expect(page.locator(".pull-detail")).toBeVisible();

    const trigger = page
      .locator(".kanban-select--header .select-dropdown-trigger")
      .first();
    await expect(trigger).toHaveText("New");

    await trigger.click();
    const statePath = `/api/v1/pulls/github/${target.owner}/${target.repo}/${target.number}/state`;
    const updateResponse = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === statePath && response.request().method() === "PUT";
    });
    await page.getByRole("option", { name: target.label, exact: true }).click();
    expect((await updateResponse).status()).toBe(200);
    await expect(trigger).toHaveText(target.label);

    const storedStatus = await page.evaluate(async (path) => {
      const response = await fetch(path);
      const body = await response.json();
      return body.merge_request.KanbanStatus;
    }, `/api/v1/pulls/github/${target.owner}/${target.repo}/${target.number}`);
    expect(storedStatus).toBe(target.status);

    await page.reload();
    await expect(page.locator(".pull-detail")).toBeVisible();
    await expect(
      page.locator(".kanban-select--header .select-dropdown-trigger").first(),
    ).toHaveText(target.label);
  } finally {
    await server.stop();
  }
});
