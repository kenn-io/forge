import { expect, request as playwrightRequest, test, type APIRequestContext } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";
import { openSettingsPanel } from "./support/settingsPanel";

let isolatedServer: IsolatedE2EServer | undefined;
let api: APIRequestContext | undefined;

test.describe.configure({ mode: "serial" });

test.beforeAll(async () => {
  isolatedServer = await startIsolatedE2EServer();
  api = await playwrightRequest.newContext({
    baseURL: isolatedServer.info.base_url,
  });
});

test.afterAll(async () => {
  await api?.dispose();
  await isolatedServer?.stop();
});

type ActivitySettings = {
  view_mode: string;
  time_range: string;
};

test("activity default view mode and time range persist through the segmented controls", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await openSettingsPanel(page, "Activity");

  const viewModeGroup = page.getByRole("radiogroup", { name: "Default view mode" });
  const timeRangeGroup = page.getByRole("radiogroup", { name: "Default time range" });
  await expect(viewModeGroup.getByRole("radio", { name: "Flat" })).toBeChecked();

  const viewModeSave = page.waitForResponse(
    (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
  );
  await viewModeGroup.getByRole("radio", { name: "Threaded" }).click();
  expect((await viewModeSave).status()).toBe(200);

  const timeRangeSave = page.waitForResponse(
    (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
  );
  await timeRangeGroup.getByRole("radio", { name: "30d" }).click();
  expect((await timeRangeSave).status()).toBe(200);

  const settingsResponse = await api!.get("/api/v1/settings");
  expect(settingsResponse.ok()).toBe(true);
  const settings = (await settingsResponse.json()) as { activity: ActivitySettings };
  expect(settings.activity.view_mode).toBe("threaded");
  expect(settings.activity.time_range).toBe("30d");

  await page.reload();
  await openSettingsPanel(page, "Activity");
  await expect(
    page.getByRole("radiogroup", { name: "Default view mode" }).getByRole("radio", { name: "Threaded" }),
  ).toBeChecked();
  await expect(
    page.getByRole("radiogroup", { name: "Default time range" }).getByRole("radio", { name: "30d" }),
  ).toBeChecked();
});
