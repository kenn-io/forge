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
      localStorage.setItem("kenn-forge-sidebar-width", "900");
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

test("the sidebar drags past 600px, keeps room for the detail pane, and remembers its width", async ({ page }) => {
  await mockTableApi(page);
  await page.addInitScript(() => {
    if (sessionStorage.getItem("kenn-forge:test:sidebar-width:init") === "1") return;
    localStorage.setItem("kenn-forge-sidebar-width", "600");
    sessionStorage.setItem("kenn-forge:test:sidebar-width:init", "1");
  });
  await page.goto("/pulls");
  const sidebar = page.locator(".kit-sidebar-layout__sidebar");
  const handle = page.getByRole("separator", { name: "Resize sidebar" });
  const sidebarWidth = async () => Math.round((await sidebar.boundingBox())!.width);
  const dragBy = async (delta: number) => {
    const box = (await handle.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + 40);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 + delta, box.y + 40, { steps: 4 });
    await page.mouse.up();
  };
  await expect.poll(sidebarWidth).toBe(600);

  await dragBy(300);
  await expect.poll(sidebarWidth).toBe(900);
  await page.reload();
  await expect.poll(sidebarWidth).toBe(900);

  // Dragged to the window's right edge, the sidebar stops where 360px is left for the
  // detail pane, so the handle stays reachable.
  await dragBy(530);
  await expect.poll(sidebarWidth).toBe(1080);
  await page.setViewportSize({ width: 1000, height: 900 });
  await expect.poll(sidebarWidth).toBe(640);
  await dragBy(-140);
  await expect.poll(sidebarWidth).toBe(500);
});

test("a default-width sidebar keeps its width in a window too narrow to reserve the detail pane", async ({ page }) => {
  await page.setViewportSize({ width: 660, height: 900 });
  await mockTableApi(page);
  await page.addInitScript(() => localStorage.setItem("kenn-forge-sidebar-width", "340"));
  await page.goto("/pulls");
  const sidebar = page.locator(".kit-sidebar-layout__sidebar");
  await expect(page.locator(".pull-item").first()).toBeVisible();
  await expect.poll(async () => Math.round((await sidebar.boundingBox())!.width)).toBe(340);
});

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
    await expect(page.locator(".item-table-headers")).toHaveCount(0);
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
    await expect(page.locator(".item-table-headers")).toBeVisible();
    box = (await handle.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + 40);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 - 1, box.y + 40);
    await page.mouse.up();
    await expect(page.locator(".pull-item--table")).toHaveCount(0);
    expect(await page.evaluate(() => localStorage.getItem("kenn-forge:pullListLayout"))).toBe("table");
  });
}

for (const [width, headers] of [
  [340, /^\s*Repo\s+Item\s+Status\s*$/],
  [560, /^\s*Repo\s+Item\s+Status\s*$/],
  [561, /^\s*Repo\s+Author\s+Item\s+Status\s+When\s*$/],
  [719, /^\s*Repo\s+Author\s+Item\s+Status\s+When\s*$/],
] as const) {
  test(`Table keeps each pull request on one row at ${width}px`, async ({ page }, testInfo) => {
    await mockTableApi(page);
    const pull = {
      ...mockPullFixture(42),
      Number: 1234,
      Title: "Improve cache refresh across repositories and background tasks",
      Author: "user-a",
      labels,
      ReviewDecision: "APPROVED",
      stack: { position: 2, size: 3 },
      CIChecksJSON: JSON.stringify([{ name: "test", status: "completed", conclusion: "failure" }]),
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
    await expect(row).toHaveClass(/pull-item--table/);
    await expect(page.locator(".item-table-headers")).toHaveText(headers);
    const title = (await row.locator(".cell--title").boundingBox())!;
    const middle = title.y + title.height / 2;
    for (const cell of [".cell--repo", ".cell--status", ".cell--link"]) {
      const box = (await row.locator(cell).boundingBox())!;
      expect(box.y, cell).toBeLessThan(middle);
      expect(box.y + box.height, cell).toBeGreaterThan(middle);
    }
    const table = page.locator(".pull-table");
    expect(await table.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath("one-row.png"), clip: { x: 0, y: 0, width, height: 240 } });
  });
}

for (const [width, grouping] of [
  [720, "flat"],
  [1000, "flat"],
  [1080, "flat"],
  [720, "byRepo"],
] as const) {
  test(`table gives the title its own column at ${width}px, ${grouping}`, async ({ page }, testInfo) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await mockTableApi(page);
    const pull = {
      ...mockPullFixture(42),
      Number: 1234,
      IsDraft: true,
      Title: "Improve cache refresh across repositories and background tasks",
      Author: "user-with-a-long-name",
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
    // Everything a two-line row reports is still on the one line.
    const status = row.locator(".cell--status");
    await expect(status.locator(".review-indicator")).toBeVisible();
    await expect(status.locator(".stack-indicator")).toBeVisible();
    await expect(status.locator('[data-testid="ci-token-failed"]')).toBeVisible();
    await expect(status.locator(".conflict-icon")).toBeVisible();
    await expect(status.locator(".star-icon--active")).toBeVisible();
    await expect(row.locator(".agent-state").getByText("Approval", { exact: true })).toBeVisible();
    await expect(row.locator(".cell--title").getByText("Draft", { exact: true })).toBeVisible();
    await expect(row.locator(".cell--author")).toHaveText("user-with-a-long-name");
    await expect(row.locator(".cell--time")).toBeVisible();

    // Status has its own column, so no row's status takes room from a title.
    const headers = page.locator(".item-table-headers");
    const ordinary = page.locator(".pull-item--table").filter({ hasNotText: pull.Title });
    const titleCell = (await row.locator(".cell--title").boundingBox())!;
    const statusCell = (await status.boundingBox())!;
    expect(statusCell.x).toBeGreaterThanOrEqual(titleCell.x + titleCell.width);
    for (const column of [".cell--author", ".cell--title", ".cell--status", ".cell--time"]) {
      const busy = (await row.locator(column).boundingBox())!;
      const calm = (await ordinary.locator(column).boundingBox())!;
      const header = (await headers.locator(column).boundingBox())!;
      expect(Math.abs(busy.x - calm.x), column).toBeLessThan(1);
      expect(Math.abs(busy.width - calm.width), column).toBeLessThan(1);
      expect(Math.abs(busy.x - header.x), column).toBeLessThan(1);
    }

    // Item is the widest column and keeps its floor even in the narrowest table, where this
    // busy row would otherwise cost it; the author shortens first. With room, the author is
    // not cut to fit a fixed column. Labels take only the room the title leaves.
    const overflows = (selector: string) => row.locator(selector).evaluate((el) => el.scrollWidth > el.clientWidth);
    for (const column of [".cell--author", ".cell--status", ".cell--time"]) {
      expect((await row.locator(column).boundingBox())!.width, column).toBeLessThan(titleCell.width);
    }
    const rem = await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).fontSize));
    expect(titleCell.width).toBeGreaterThanOrEqual(18 * rem - 1);
    if (width >= 1000) {
      expect(await overflows(".cell--author")).toBe(false);
    }
    // This busy row needs more than 1000px for the full title plus a label.
    if (width >= 1080) {
      expect(await overflows(".item-title")).toBe(false);
      await expect(row.locator(".label-row").getByText("bug", { exact: true })).toBeVisible();
    } else {
      await expect(row.locator(".label-more")).toHaveText("+4");
    }

    await row.click();
    await expect(row).toHaveClass(/selected/);
    await page.screenshot({ path: testInfo.outputPath("table.png"), clip: { x: 0, y: 0, width, height: 280 } });
    expect(errors).toEqual([]);
  });
}

test("a focused row keeps focus when a narrower sidebar turns the table into rows", async ({ page }) => {
  await page.setViewportSize({ width: 1200, height: 900 });
  await mockTableApi(page);
  await page.addInitScript(() => {
    localStorage.setItem("kenn-forge:pullListLayout", "table");
    localStorage.setItem("kenn-forge:groupingMode", "flat");
    localStorage.setItem("kenn-forge-sidebar-width", "800");
  });
  await page.goto("/pulls");
  const row = page.locator(".pull-item").first();
  await expect(row).toHaveClass(/pull-item--table/);
  await row.focus();
  await expect(row).toBeFocused();

  const handle = page.getByRole("separator", { name: "Resize sidebar" });
  const dragBy = async (delta: number) => {
    const box = (await handle.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + 40);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 + delta, box.y + 40, { steps: 4 });
    await page.mouse.up();
  };
  // 330px is under the table's minimum.
  await dragBy(-470);
  await expect(page.locator(".pull-item--table")).toHaveCount(0);
  await expect(row).toBeFocused();
  await dragBy(470);
  await expect(row).toHaveClass(/pull-item--table/);
  await expect(row).toBeFocused();
});

test("a focused stack control keeps focus when dragging the sidebar changes the layout", async ({ page }) => {
  await mockTableApi(page);
  const pulls = [
    {
      ...mockPullFixture(42),
      ID: 800,
      Number: 1234,
      Title: "Stack root",
      stack: { stack_id: 71, position: 1, size: 2 },
    },
    {
      ...mockPullFixture(42),
      ID: 801,
      Number: 1235,
      Title: "Stack child",
      stack: { stack_id: 71, position: 2, size: 2 },
    },
  ];
  await page.route("**/api/v1/pulls**", async (route) => {
    if (new URL(route.request().url()).pathname !== "/api/v1/pulls") return route.fallback();
    await route.fulfill({ json: pulls });
  });
  await page.addInitScript(() => {
    localStorage.setItem("kenn-forge:pullListLayout", "table");
    localStorage.setItem("kenn-forge:groupingMode", "flat");
    localStorage.setItem("kenn-forge:pullStackTree", "1");
    localStorage.setItem("kenn-forge-sidebar-width", "800");
  });
  await page.goto("/pulls");
  const toggle = page.getByRole("button", { name: /^(Expand|Collapse) stack at #1234: 2 PRs in stack$/ });
  const handle = page.getByRole("separator", { name: "Resize sidebar" });
  const dragBy = async (delta: number) => {
    const box = (await handle.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + 40);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 + delta, box.y + 40, { steps: 4 });
    await page.mouse.up();
  };
  await toggle.focus();
  await expect(toggle).toBeFocused();

  await dragBy(-470);
  await expect(page.locator(".pull-item--table")).toHaveCount(0);
  await expect(toggle).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");

  await dragBy(470);
  await expect(page.locator(".pull-item--table").first()).toBeVisible();
  await expect(toggle).toBeFocused();
});

test("Stack switched off leaves the table only the control that expands a stack", async ({ page }) => {
  await mockTableApi(page);
  const pulls = [
    {
      ...mockPullFixture(42),
      ID: 900,
      Number: 1301,
      Title: "Second of three",
      stack: { stack_id: 71, position: 2, size: 3 },
    },
    {
      ...mockPullFixture(42),
      ID: 901,
      Number: 1302,
      Title: "Third of three",
      stack: { stack_id: 71, position: 3, size: 3 },
    },
    {
      ...mockPullFixture(42),
      ID: 902,
      Number: 1303,
      Title: "Lone member",
      stack: { stack_id: 72, position: 2, size: 3 },
    },
  ];
  await page.route("**/api/v1/pulls**", async (route) => {
    if (new URL(route.request().url()).pathname !== "/api/v1/pulls") return route.fallback();
    await route.fulfill({ json: pulls });
  });
  await page.addInitScript(() => {
    localStorage.setItem("kenn-forge:pullListLayout", "table");
    localStorage.setItem("kenn-forge:groupingMode", "flat");
    localStorage.setItem("kenn-forge:pullStackTree", "1");
    localStorage.setItem("kenn-forge-sidebar-width", "900");
  });
  await page.goto("/pulls");
  const list = page.locator(".pull-table");
  const toggle = page.getByRole("button", { name: /^(Expand|Collapse) stack at #1301: 2 of 3 PRs in this view$/ });
  await expect(toggle).toHaveText("2");
  await expect(list).toContainText("starts at 2/3");
  await expect(list.locator(".stack-partial")).toHaveText("1/3");

  await page.evaluate(() => localStorage.setItem("kenn-forge:pullTableHiddenParts", JSON.stringify(["stack"])));
  await page.reload();
  await expect(page.getByText("Lone member", { exact: true })).toBeVisible();
  await expect(toggle).toHaveText("");
  await expect(list).not.toContainText("PRs in this view");
  await expect(list).not.toContainText("/3");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByText("Third of three", { exact: true })).toBeVisible();
});

test("a large text size never makes the table wider than its sidebar", async ({ page }) => {
  await mockTableApi(page);
  await page.addInitScript(() => {
    localStorage.setItem("kenn-forge:pullListLayout", "table");
    localStorage.setItem("kenn-forge:groupingMode", "flat");
    localStorage.setItem("kenn-forge-sidebar-width", "720");
  });
  await page.goto("/pulls");
  const table = page.locator(".pull-table");
  await expect(table.locator(".pull-item--table").first()).toBeVisible();
  await page.evaluate(() => (document.documentElement.style.fontSize = "40px"));
  const box = (await table.boundingBox())!;
  const link = (await table.locator(".pull-item--table .cell--link").first().boundingBox())!;
  expect(link.x + link.width).toBeLessThanOrEqual(box.x + box.width + 1);
  expect(await table.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
});

for (const [name, hidden] of [
  ["only Author", ["author"]],
  ["every part", ["repo", "author", "labels", "ci", "review", "stack", "star", "when"]],
] as const) {
  test(`a table with ${name} hidden drops it and gives its room to the title`, async ({ page }) => {
    await mockTableApi(page);
    const pull = {
      ...mockPullFixture(42),
      Number: 1234,
      IsDraft: true,
      Title: "Improve cache refresh across repositories and background tasks",
      Author: "user-with-a-long-name",
      Starred: true,
      labels,
      ReviewDecision: "APPROVED",
      MergeableState: "dirty",
      stack: { position: 2, size: 3 },
      CIChecksJSON: JSON.stringify([{ name: "test", status: "completed", conclusion: "failure" }]),
    };
    await page.route("**/api/v1/pulls**", async (route) => {
      if (new URL(route.request().url()).pathname !== "/api/v1/pulls") return route.fallback();
      await route.fulfill({ json: [pull, { ...mockPullFixture(84), Author: "user-b" }] });
    });
    await page.addInitScript(() => {
      localStorage.setItem("kenn-forge:pullListLayout", "table");
      localStorage.setItem("kenn-forge:groupingMode", "flat");
      localStorage.setItem("kenn-forge-sidebar-width", "720");
    });
    await page.goto("/pulls");
    const row = page.locator(".pull-item--table").filter({ hasText: pull.Title });
    const headers = page.locator(".item-table-headers");
    const titleWidth = async () => (await row.locator(".cell--title").boundingBox())!.width;
    await expect(headers).toHaveText(/Repo\s*Author\s*Item\s*Status\s*When/);
    const fullWidth = await titleWidth();

    await page.evaluate((hidden) => {
      localStorage.setItem("kenn-forge:pullTableHiddenParts", JSON.stringify(hidden));
    }, hidden);
    await page.reload();
    await expect(row).toBeVisible();

    const parts = {
      repo: row.locator(".cell--repo"),
      author: row.locator(".cell--author"),
      labels: row.locator(".label-row"),
      ci: row.locator('[data-testid="ci-token-failed"]'),
      review: row.locator(".review-indicator"),
      stack: row.locator(".stack-indicator"),
      star: row.getByRole("button", { name: "Unstar", exact: true }),
      when: row.locator(".cell--time"),
    };
    for (const [part, locator] of Object.entries(parts)) {
      await expect(locator, part).toHaveCount((hidden as readonly string[]).includes(part) ? 0 : 1);
    }
    await expect(headers).toHaveText(
      hidden.length === 1 ? /^\s*Repo\s*Item\s*Status\s*When\s*$/ : /^\s*Item\s*Status\s*$/,
    );
    // What a hidden part never covered still shows.
    await expect(row.locator(".cell--title").getByText("Draft", { exact: true })).toBeVisible();
    await expect(row.locator(".conflict-icon")).toBeVisible();
    await expect(row.getByText(`#${pull.Number}`, { exact: true })).toBeVisible();

    // The remaining cells stay under their headers, and the title takes the freed room.
    for (const column of [".cell--title", ".cell--status"]) {
      const cell = (await row.locator(column).boundingBox())!;
      const header = (await headers.locator(column).boundingBox())!;
      expect(Math.abs(cell.x - header.x), column).toBeLessThan(1);
    }
    expect(await titleWidth()).toBeGreaterThan(fullWidth + 60);
  });
}

for (const width of [720, 1000]) {
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
      const headers = page.locator(".item-table-headers");
      for (const title of ["Stack root", "Stack child", "Ordinary pull"]) {
        const row = page.locator(".pull-item--table").filter({ hasText: title });
        // A stack member indents inside its Item cell; the columns themselves stay put.
        for (const column of [".cell--author", ".cell--title", ".cell--status", ".cell--time"]) {
          const cell = (await row.locator(column).boundingBox())!;
          const heading = (await headers.locator(column).boundingBox())!;
          expect(Math.abs(cell.x - heading.x), `${title} ${column}`).toBeLessThan(1);
          expect(Math.abs(cell.width - heading.width), `${title} ${column}`).toBeLessThan(1);
        }
        await expect(row.locator(".item-title")).toHaveText(title);
        await expect(row.getByRole("button", { name: "Open pull request in provider" })).toBeVisible();
      }
      const rootRef = (await page
        .locator(".pull-item--table")
        .filter({ hasText: "Stack root" })
        .locator(".cell--title > :first-child")
        .boundingBox())!;
      const childRef = (await page
        .locator(".pull-item--table")
        .filter({ hasText: "Stack child" })
        .locator(".item-ref")
        .boundingBox())!;
      expect(childRef.x).toBeGreaterThan(rootRef.x);
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
