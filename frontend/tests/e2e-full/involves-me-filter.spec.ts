import { expect, test, type Page } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

const storageKeys = {
  pulls: "kenn-forge:filters:pulls:involves-me",
  issues: "kenn-forge:filters:issues:involves-me",
  activity: "kenn-forge:filters:activity:involves-me",
} as const;

async function selectFilter(page: Page, trigger: string): Promise<void> {
  await page.locator(trigger).click();
  await page.locator(".kit-filter-dropdown__item", { hasText: "Involves me" }).click();
}

function waitForInvolvesMeRequest(page: Page, path: string): ReturnType<Page["waitForResponse"]> {
  return page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === path && url.searchParams.get("involves_me") === "true";
  });
}

function waitForUnassignedRequest(page: Page, path: string): ReturnType<Page["waitForResponse"]> {
  return page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === path && url.searchParams.get("unassigned") === "true";
  });
}

async function expectFilterItemInViewport(page: Page, label: string): Promise<void> {
  const item = page.locator(".kit-filter-dropdown__item", { hasText: label });
  await expect(item).toBeVisible();
  const box = await item.boundingBox();
  const viewport = page.viewportSize();
  expect(box).not.toBeNull();
  expect(viewport).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(viewport!.width);
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.y + box!.height).toBeLessThanOrEqual(viewport!.height);
}

test.describe("Involves me standard filters", () => {
  let server: IsolatedE2EServer | undefined;

  test.beforeAll(async () => {
    server = await startIsolatedE2EServer();
  });

  test.afterAll(async () => {
    await server?.stop();
  });

  test("persists independently in Pulls, Issues, and Activity", async ({ page }) => {
    const baseURL = server!.info.base_url;

    await page.goto(`${baseURL}/pulls`);
    await expect(page.locator(".pull-item").first()).toBeVisible();
    const pullsResponse = waitForInvolvesMeRequest(page, "/api/v1/pulls");
    await selectFilter(page, ".compact-filter-menu .kit-filter-dropdown__btn");
    const pullsFiltered = await pullsResponse;
    expect(pullsFiltered.ok(), await pullsFiltered.text()).toBe(true);
    await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), storageKeys.pulls)).toBe("1");

    const persistedPullsResponse = waitForInvolvesMeRequest(page, "/api/v1/pulls");
    await page.reload();
    expect((await persistedPullsResponse).ok()).toBe(true);

    await page.goto(`${baseURL}/issues`);
    await expect(page.locator(".issue-item").first()).toBeVisible();
    expect(await page.evaluate((key) => localStorage.getItem(key), storageKeys.issues)).toBeNull();
    const issuesResponse = waitForInvolvesMeRequest(page, "/api/v1/issues");
    await selectFilter(page, ".compact-filter-menu .kit-filter-dropdown__btn");
    expect((await issuesResponse).ok()).toBe(true);

    await page.goto(baseURL);
    await expect(page.locator(".activity-row").first()).toBeVisible();
    expect(await page.evaluate((key) => localStorage.getItem(key), storageKeys.activity)).toBeNull();
    const activityResponse = waitForInvolvesMeRequest(page, "/api/v1/activity");
    await selectFilter(page, ".activity-feed .kit-filter-dropdown__btn");
    expect((await activityResponse).ok()).toBe(true);

    expect(
      await page.evaluate((keys) => keys.map((key) => localStorage.getItem(key)), Object.values(storageKeys)),
    ).toEqual(["1", "1", "1"]);
  });

  test("uses the same pull and issue filter from the phone list presentation", async ({ page }) => {
    const baseURL = server!.info.base_url;
    await page.setViewportSize({ width: 390, height: 844 });

    await page.goto(`${baseURL}/m/pulls`);
    await expect(page.locator(".focus-list .pull-item").first()).toBeVisible();
    await page.getByRole("button", { name: "Filters" }).click();
    const pullsResponse = waitForInvolvesMeRequest(page, "/api/v1/pulls");
    await page.getByRole("button", { name: "Involves me" }).click();
    expect((await pullsResponse).ok()).toBe(true);
    await expect(page.getByRole("button", { name: "Involves me" })).toHaveAttribute("aria-pressed", "true");

    await page.goto(`${baseURL}/m/issues`);
    await expect(page.locator(".focus-list .issue-item").first()).toBeVisible();
    await page.getByRole("button", { name: "Filters" }).click();
    const issuesResponse = waitForInvolvesMeRequest(page, "/api/v1/issues");
    await page.getByRole("button", { name: "Involves me" }).click();
    expect((await issuesResponse).ok()).toBe(true);
    await expect(page.getByRole("button", { name: "Involves me" })).toHaveAttribute("aria-pressed", "true");
  });

  test("keeps Unassigned reachable in the regular Pulls, Issues, and Activity menus", async ({ page }) => {
    const baseURL = server!.info.base_url;

    await page.goto(`${baseURL}/pulls`);
    await expect(page.locator(".pull-item").first()).toBeVisible();
    await page.locator(".compact-filter-menu .kit-filter-dropdown__btn").click();
    await expectFilterItemInViewport(page, "Unassigned");
    const pullsResponse = waitForUnassignedRequest(page, "/api/v1/pulls");
    await page.locator(".kit-filter-dropdown__item", { hasText: "Unassigned" }).click();
    expect((await pullsResponse).ok()).toBe(true);

    await page.goto(`${baseURL}/issues`);
    await expect(page.locator(".issue-item").first()).toBeVisible();
    await page.locator(".compact-filter-menu .kit-filter-dropdown__btn").click();
    await expectFilterItemInViewport(page, "Unassigned");
    const issuesResponse = waitForUnassignedRequest(page, "/api/v1/issues");
    await page.locator(".kit-filter-dropdown__item", { hasText: "Unassigned" }).click();
    expect((await issuesResponse).ok()).toBe(true);

    await page.goto(baseURL);
    await expect(page.locator(".activity-row").first()).toBeVisible();
    await page.locator(".activity-feed .kit-filter-dropdown__btn").click();
    await expectFilterItemInViewport(page, "Unassigned");
    const activityResponse = waitForUnassignedRequest(page, "/api/v1/activity");
    await page.locator(".kit-filter-dropdown__item", { hasText: "Unassigned" }).click();
    expect((await activityResponse).ok()).toBe(true);
  });

  test("a second click asks the server for the opposite filter and persists it", async ({ page }) => {
    const baseURL = server!.info.base_url;
    const waitForQuery = (path: string, param: string) =>
      page.waitForResponse((response) => {
        const url = new URL(response.url());
        return url.pathname === path && url.searchParams.get(param) === "true";
      });

    for (const surface of [
      {
        route: `${baseURL}/pulls`,
        row: ".pull-item",
        trigger: ".compact-filter-menu .kit-filter-dropdown__btn",
        path: "/api/v1/pulls",
        key: "pulls",
      },
      {
        route: `${baseURL}/issues`,
        row: ".issue-item",
        trigger: ".compact-filter-menu .kit-filter-dropdown__btn",
        path: "/api/v1/issues",
        key: "issues",
      },
      {
        route: baseURL,
        row: ".activity-row",
        trigger: ".activity-feed .kit-filter-dropdown__btn",
        path: "/api/v1/activity",
        key: "activity",
      },
    ] as const) {
      await page.goto(surface.route);
      await expect(page.locator(surface.row).first()).toBeVisible();
      await page.locator(surface.trigger).click();

      for (const filter of [
        { label: "Involves me", on: "involves_me", off: "not_involves_me", storage: "involves-me" },
        { label: "Unassigned", on: "unassigned", off: "assigned", storage: "unassigned" },
      ]) {
        const item = page.locator(".kit-filter-dropdown__item", { hasText: filter.label });
        const required = waitForQuery(surface.path, filter.on);
        await item.click();
        expect((await required).ok()).toBe(true);

        const excluded = waitForQuery(surface.path, filter.off);
        await item.click();
        const response = await excluded;
        expect(response.ok(), await response.text()).toBe(true);
        expect(new URL(response.url()).searchParams.get(filter.on)).toBeNull();
        await expect(item).toHaveAccessibleName(/, excluded$/);
        const storageKey = `kenn-forge:filters:${surface.key}:${filter.storage}`;
        await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), storageKey)).toBe("0");

        // The third click turns the filter off again.
        await item.click();
        await expect(item).toHaveAttribute("aria-pressed", "false");
        await expect.poll(() => page.evaluate((key) => localStorage.getItem(key), storageKey)).toBeNull();
      }
    }
  });

  test("collapses pull and issue controls into the filter menu on compact canonical routes", async ({ page }) => {
    const baseURL = server!.info.base_url;
    await page.setViewportSize({ width: 390, height: 844 });

    for (const list of [
      { route: "pulls", item: ".pull-item" },
      { route: "issues", item: ".issue-item" },
    ]) {
      await page.goto(`${baseURL}/${list.route}`);
      await expect(page.locator(`.focus-list ${list.item}`).first()).toBeVisible();
      expect(new URL(page.url()).pathname).toBe(`/${list.route}`);
      const trigger = page.locator(".focus-list .header .kit-filter-dropdown__btn");
      await expect(trigger).toBeVisible();
      await trigger.click();
      await expectFilterItemInViewport(page, "Unassigned");
    }
  });
});
