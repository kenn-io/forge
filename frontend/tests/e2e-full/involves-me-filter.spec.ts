import { expect, test, type Page } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

const storageKeys = {
  pulls: "kenn-forge:filters:pulls:involves-me",
  issues: "kenn-forge:filters:issues:involves-me",
  activity: "kenn-forge:filters:activity:involves-me",
} as const;

async function selectFilter(page: Page, trigger: string): Promise<void> {
  await page.locator(trigger).click();
  await page.locator(".kit-filter-dropdown__item, .activity-filters__item", { hasText: "Involves me" }).click();
}

function waitForInvolvesMeRequest(page: Page, path: string): ReturnType<Page["waitForResponse"]> {
  return page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === path && url.searchParams.get("involves_me") === "true";
  });
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
    await selectFilter(page, ".activity-filters__trigger");
    expect((await activityResponse).ok()).toBe(true);

    expect(
      await page.evaluate((keys) => keys.map((key) => localStorage.getItem(key)), Object.values(storageKeys)),
    ).toEqual(["1", "1", "1"]);
  });
});
