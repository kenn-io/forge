import { expect, test, type Page } from "@playwright/test";

import { mockPullFixture } from "../../src/test/mockApiFetch";
import { mockApi, mockSettings } from "./support/mockApi";

test.use({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });

const labels = [
  { name: "bug", color: "d73a4a" },
  { name: "frontend", color: "0075ca" },
  { name: "ready", color: "008672" },
  { name: "priority", color: "e4e669" },
];

async function mockTableApi(page: Page): Promise<void> {
  await mockApi(page);
  const settings = structuredClone(mockSettings);
  settings.workspaces.show_agent_status_in_lists = true;
  await page.route("**/api/v1/settings", (route) => route.fulfill({ json: settings }));
}

for (const layout of ["rows", "table"]) {
  test(`star controls preserve priority without selecting the PR in ${layout}`, async ({ page }) => {
    await mockTableApi(page);
    const pull = {
      ...mockPullFixture(42),
      Title: "Prioritized cache refresh",
      Author: "user-a",
      Starred: true,
      labels,
    };
    const mutations: string[] = [];
    await page.route("**/api/v1/pulls**", async (route) => {
      if (new URL(route.request().url()).pathname !== "/api/v1/pulls") return route.fallback();
      await route.fulfill({ json: [pull] });
    });
    await page.route("**/api/v1/starred**", async (route) => {
      const request = route.request();
      const expected = {
        item_type: "pr",
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
      };
      if (request.method() === "DELETE") {
        expect(Object.fromEntries(new URL(request.url()).searchParams)).toEqual({ ...expected, number: "42" });
        pull.Starred = false;
      } else if (request.method() === "PUT") {
        expect(request.postDataJSON()).toEqual({ ...expected, number: 42 });
        pull.Starred = true;
      } else {
        return route.fallback();
      }
      mutations.push(request.method());
      await route.fulfill({ status: 204, body: "" });
    });
    await page.addInitScript((layout) => {
      localStorage.setItem("kenn-forge:pullListLayout", layout);
      localStorage.setItem("kenn-forge:groupingMode", "flat");
      localStorage.setItem("kenn-forge-sidebar-width", "600");
    }, layout);
    await page.goto("/pulls");
    const row = page.locator(".pull-item").filter({ hasText: pull.Title });
    await expect(row.locator(".star-icon--active")).toBeVisible();
    await row.getByRole("button", { name: "Unstar", exact: true }).click();
    await expect(row.locator(".star-icon--active")).toHaveCount(0);
    await expect.poll(() => mutations).toEqual(["DELETE"]);
    const star = row.getByRole("button", { name: "Star", exact: true });
    await star.focus();
    await page.keyboard.press("Space");
    await expect(row.locator(".star-icon--active")).toBeVisible();
    await expect.poll(() => mutations).toEqual(["DELETE", "PUT"]);
    await expect(row).not.toHaveClass(/\bselected\b/);
    expect(new URL(page.url()).pathname).toBe("/pulls");
  });
}

for (const width of [200, 339]) {
  test(`Table uses readable two-line rows at ${width}px and returns when widened`, async ({ page }, testInfo) => {
    await mockTableApi(page);
    const pull = {
      ...mockPullFixture(42),
      Number: 1234,
      Title: "Improve cache refresh",
      Author: "user-a",
      IsDraft: true,
      workspace: { id: "workspace-a", status: "ready", agent_state: "approval" },
      ReviewDecision: "APPROVED",
      MergeableState: "dirty",
      stack: { position: 2, size: 3 },
      CIChecksJSON: JSON.stringify([
        { name: "build", status: "completed", conclusion: "success" },
        { name: "test", status: "completed", conclusion: "failure" },
        { name: "lint", status: "in_progress", conclusion: "" },
      ]),
    };
    await page.route("**/api/v1/pulls**", async (route) => {
      if (new URL(route.request().url()).pathname !== "/api/v1/pulls") return route.fallback();
      await route.fulfill({ json: [pull] });
    });
    await page.addInitScript((width) => {
      localStorage.setItem("kenn-forge:pullListLayout", "table");
      localStorage.setItem("kenn-forge:groupingMode", "flat");
      localStorage.setItem("kenn-forge-sidebar-width", String(width));
    }, width);
    await page.goto("/pulls");
    const row = page.locator(".pull-item").filter({ hasText: pull.Title });
    await expect(row).toBeVisible();
    await expect(page.locator(".pull-item--table")).toHaveCount(0);
    await expect(page.locator(".table-headers")).toHaveCount(0);
    await expect(row.locator(".meta-row")).toBeVisible();
    expect((await row.locator(".title-text").boundingBox())!.width).toBeGreaterThanOrEqual(40);
    const rowBox = (await row.boundingBox())!;
    for (const selector of [
      ".review-indicator",
      ".stack-indicator",
      '[data-testid="ci-token-failed"]',
      ".conflict-icon",
    ]) {
      const icon = row.locator(selector);
      await expect(icon).toBeVisible();
      const box = (await icon.boundingBox())!;
      expect(box.x + box.width).toBeLessThanOrEqual(rowBox.x + rowBox.width);
    }
    await page.screenshot({ path: testInfo.outputPath("narrow-rows.png"), clip: { x: 0, y: 0, width, height: 280 } });
    const handle = page.getByRole("separator", { name: "Resize sidebar" });
    let box = (await handle.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + 40);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 + 340 - width, box.y + 40);
    await page.mouse.up();
    await expect(row).toHaveClass(/pull-item--table/);
    await expect(page.locator(".table-headers")).toBeVisible();
    box = (await handle.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + 40);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 - 1, box.y + 40);
    await page.mouse.up();
    await expect(page.locator(".pull-item--table")).toHaveCount(0);
    expect(await page.evaluate(() => localStorage.getItem("kenn-forge:pullListLayout"))).toBe("table");
  });
}

for (const [width, grouping] of [
  [340, "flat"],
  [431, "flat"],
  [560, "flat"],
  [561, "flat"],
  [600, "flat"],
  [340, "byRepo"],
] as const) {
  test(`table keeps titles and statuses visible at ${width}px, ${grouping}`, async ({ page }, testInfo) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await mockTableApi(page);
    const pull = {
      ...mockPullFixture(42),
      Number: 1234,
      IsDraft: true,
      Title: "Improve cache refresh across repositories and background tasks",
      Author: "user-a",
      Starred: true,
      labels,
      workspace: { id: "workspace-a", status: "ready", agent_state: "approval" },
      ReviewDecision: "APPROVED",
      MergeableState: "dirty",
      stack: { position: 2, size: 3 },
      CIChecksJSON: JSON.stringify([
        { name: "build", status: "completed", conclusion: "success" },
        { name: "test", status: "completed", conclusion: "failure" },
        { name: "lint", status: "in_progress", conclusion: "" },
      ]),
    };
    await page.route("**/api/v1/pulls**", async (route) => {
      if (new URL(route.request().url()).pathname !== "/api/v1/pulls") return route.fallback();
      await route.fulfill({ json: [pull, { ...mockPullFixture(84), Author: "user-b" }] });
    });
    await page.addInitScript(
      ({ width, grouping }) => {
        localStorage.setItem("kenn-forge:pullListLayout", "table");
        localStorage.setItem("kenn-forge:groupingMode", grouping);
        localStorage.setItem("kenn-forge-sidebar-width", String(width));
      },
      { width, grouping },
    );
    await page.goto("/pulls");
    const row = page.locator(".pull-item--table").filter({ hasText: pull.Title });
    await expect(row).toBeVisible();
    await page.evaluate(() => document.fonts.ready);
    if (grouping === "flat") {
      await expect(row.locator(".repo-chip__label")).toHaveText("widgets");
      await expect(row.locator(".repo-chip__label")).toHaveAttribute("title", "github.com/acme/widgets");
    }
    await expect(row.locator(".review-indicator")).toBeVisible();
    await expect(row.locator(".stack-indicator")).toBeVisible();
    await expect(row.locator('[data-testid="ci-token-failed"]')).toBeVisible();
    await expect(row.locator(".conflict-icon")).toBeVisible();
    await expect(row.locator(".agent-state").getByText("Approval", { exact: true })).toBeVisible();
    await expect(row.locator(".label-row").getByText("bug", { exact: true })).toBeVisible();
    await expect(row.locator(".label-more")).toHaveText(/^\+[2-3]$/);
    await expect(row.locator(".star-icon--active")).toBeVisible();
    await expect
      .poll(async () => row.locator(".title-text").evaluate((el) => el.getBoundingClientRect().width))
      .toBeGreaterThanOrEqual(40);
    const titleCell = await row.locator(".cell--title").boundingBox();
    const statuses = await row.locator(".row-status").boundingBox();
    expect(statuses!.x + statuses!.width).toBeLessThanOrEqual(titleCell!.x + titleCell!.width + 1);
    const title = (await row.locator(".title-text").boundingBox())!;
    const agent = (await row.locator(".agent-state").boundingBox())!;
    expect(title.x + title.width).toBeLessThanOrEqual(agent.x + 1);
    const labelRow = (await row.locator(".label-row").boundingBox())!;
    expect(labelRow.x + labelRow.width).toBeLessThanOrEqual(titleCell!.x + titleCell!.width + 1);
    const ordinary = page.locator(".pull-item--table").filter({ hasNotText: pull.Title });
    const ordinaryCell = (await ordinary.locator(".cell--title").boundingBox())!;
    expect(ordinaryCell.height).toBe((await ordinary.locator(".title-text").boundingBox())!.height);
    const headers = page.locator(".table-headers");
    const itemHeader = await headers.getByText("Item", { exact: true }).boundingBox();
    expect(Math.abs(titleCell!.x - itemHeader!.x)).toBeLessThan(1);
    await expect(row.getByRole("img", { name: "Draft pull request" })).toBeVisible();
    if (width <= 560) {
      await expect(row.locator(".cell--author")).toBeHidden();
      await expect(row.locator(".cell--time")).toBeHidden();
    } else {
      await expect(row.locator(".cell--author")).toBeVisible();
      await expect(row.locator(".cell--time")).toBeVisible();
    }
    await row.click();
    await expect(row).toHaveClass(/selected/);
    await page.screenshot({ path: testInfo.outputPath("table.png"), clip: { x: 0, y: 0, width, height: 280 } });
    expect(errors).toEqual([]);
  });
}

for (const width of [340, 600]) {
  for (const grouping of ["flat", "byRepo"]) {
    test(`stack tree keeps table columns aligned at ${width}px, ${grouping}`, async ({ page }, testInfo) => {
      await mockTableApi(page);
      const pulls = [
        {
          ...mockPullFixture(42),
          ID: 800,
          Number: 1234,
          Title: "Stack root",
          Author: "user-a",
          IsDraft: true,
          labels,
          Starred: true,
          stack: { stack_id: 71, position: 1, size: 2 },
          workspace: { id: "workspace-a", status: "ready", agent_state: "approval" },
          ReviewDecision: "APPROVED",
          MergeableState: "dirty",
          CIChecksJSON: JSON.stringify([
            { name: "build", status: "completed", conclusion: "success" },
            { name: "test", status: "completed", conclusion: "failure" },
            { name: "lint", status: "in_progress", conclusion: "" },
          ]),
        },
        {
          ...mockPullFixture(42),
          ID: 801,
          Number: 1235,
          Title: "Stack child",
          Author: "user-a",
          stack: { stack_id: 71, position: 2, size: 2 },
        },
        { ...mockPullFixture(42), ID: 802, Number: 1236, Title: "Ordinary pull", Author: "user-b" },
      ];
      await page.route("**/api/v1/pulls**", async (route) => {
        if (new URL(route.request().url()).pathname !== "/api/v1/pulls") return route.fallback();
        await route.fulfill({ json: pulls });
      });
      await page.addInitScript(
        ({ width, grouping }) => {
          localStorage.setItem("kenn-forge:pullListLayout", "table");
          localStorage.setItem("kenn-forge:groupingMode", grouping);
          localStorage.setItem("kenn-forge:pullStackTree", "1");
          localStorage.setItem("kenn-forge-sidebar-width", String(width));
        },
        { width, grouping },
      );
      await page.goto("/pulls");
      const toggle = page.getByRole("button", { name: /^(Expand|Collapse) stack at #1234: 2 PRs in stack$/ });
      await toggle.focus();
      await page.keyboard.press("Enter");
      await expect(toggle).toHaveAttribute("aria-expanded", "true");
      await expect(page.getByText("Stack child", { exact: true })).toBeVisible();
      await page.evaluate(() => document.fonts.ready);
      const headers = page.locator(".table-headers");
      for (const title of ["Stack root", "Stack child", "Ordinary pull"]) {
        const row = page.locator(".pull-item--table").filter({ hasText: title });
        const item = (await row.locator(".cell--title").boundingBox())!;
        const statuses = (await row.locator(".row-status").boundingBox())!;
        expect(statuses.x + statuses.width).toBeLessThanOrEqual(item.x + item.width + 1);
        expect((await row.locator(".title-text").boundingBox())!.width).toBeGreaterThanOrEqual(40);
        const heading = (await headers.getByText("Item", { exact: true }).boundingBox())!;
        expect(Math.abs(item.x - heading.x)).toBeLessThan(1);
        if (width > 560) {
          const time = (await row.locator(".cell--time").boundingBox())!;
          const when = (await headers.getByText("When", { exact: true }).boundingBox())!;
          expect(Math.abs(time.x + time.width - when.x - when.width)).toBeLessThan(1);
        }
        await expect(row.getByRole("button", { name: "Open pull request in provider" })).toBeVisible();
      }
      expect(new URL(page.url()).pathname).toBe("/pulls");
      await page.screenshot({ path: testInfo.outputPath("stack-table.png"), clip: { x: 0, y: 0, width, height: 320 } });
      await toggle.focus();
      await page.keyboard.press("Space");
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await expect(page.getByText("Stack child", { exact: true })).toHaveCount(0);
      await toggle.click();
      await expect(page.getByText("Stack child", { exact: true })).toBeVisible();
      expect(new URL(page.url()).pathname).toBe("/pulls");
    });
  }
}
