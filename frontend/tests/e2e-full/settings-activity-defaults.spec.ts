import { expect, request as playwrightRequest, test, type APIRequestContext } from "@playwright/test";
import type { SettingsResponse as GeneratedSettingsResponse } from "../../src/lib/api/generated/models/index.js";
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

type SettingsResponse = GeneratedSettingsResponse;

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
  const settings: SettingsResponse = await settingsResponse.json();
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

  await page.getByRole("button", { name: "Back to app" }).click();
  for (const label of ["Flat", "7d"]) {
    await page.locator(".activity-filters__trigger").click();
    await page.locator(".activity-filters__item", { hasText: label }).click();
  }
  await page.reload();
  await expect(page.locator(".activity-filters__trigger")).toContainText("Flat · 7d");
  await page.locator(".activity-filters__trigger").click();
  await page.getByRole("button", { name: "Roll up commits", exact: true }).click();
  await page.keyboard.press("Escape");
  await page.getByTitle("Settings", { exact: true }).click();
  await openSettingsPanel(page, "Activity");
  const activitySave = page.waitForResponse(
    (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
  );
  await page.getByRole("button", { name: "Toggle hide bots" }).click();
  expect((await activitySave).status()).toBe(200);
  await page.getByRole("button", { name: "Back to app" }).click();
  await expect(page.locator(".activity-filters__trigger")).toContainText("Flat · 7d");
  await page.locator(".activity-filters__trigger").click();
  await expect(page.getByRole("button", { name: "Roll up commits", exact: true })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
});

test("settings panels serialize writes through the shared queue", async ({ page }) => {
  const baselineResponse = await api!.get("/api/v1/settings");
  expect(baselineResponse.ok()).toBe(true);
  const baseline: SettingsResponse = await baselineResponse.json();
  const nextHideBots = !baseline.activity.hide_bots;
  const nextPreferNativeStacks = !baseline.pull_requests.prefer_github_native_stacks;

  let putRequests = 0;
  let noteFirstCommitted: () => void = () => undefined;
  let releaseFirstResponse: () => void = () => undefined;
  const firstCommitted = new Promise<void>((resolve) => {
    noteFirstCommitted = resolve;
  });
  const firstResponseGate = new Promise<void>((resolve) => {
    releaseFirstResponse = resolve;
  });

  await page.route("**/api/v1/settings", async (route) => {
    if (route.request().method() !== "PUT") {
      await route.fallback();
      return;
    }
    putRequests += 1;
    const response = await route.fetch();
    if (putRequests === 1) {
      noteFirstCommitted();
      await firstResponseGate;
    }
    await route.fulfill({ response });
  });

  try {
    await page.goto(`${isolatedServer!.info.base_url}/settings`);
    await openSettingsPanel(page, "Activity");
    const hideBots = page.getByRole("button", { name: "Toggle hide bots" });
    await expect(hideBots).toHaveAttribute("aria-pressed", String(baseline.activity.hide_bots));
    await hideBots.click();
    await expect(hideBots).toHaveAttribute("aria-pressed", String(nextHideBots));
    await firstCommitted;

    await openSettingsPanel(page, "Pull requests");
    const preferNativeStacks = page.getByRole("button", { name: "Prefer GitHub native stacks" });
    await expect(preferNativeStacks).toHaveAttribute(
      "aria-pressed",
      String(baseline.pull_requests.prefer_github_native_stacks),
    );
    await preferNativeStacks.click();
    await expect(preferNativeStacks).toHaveAttribute("aria-pressed", String(nextPreferNativeStacks));

    await page.waitForTimeout(100);
    expect(putRequests).toBe(1);

    releaseFirstResponse();
    await expect.poll(() => putRequests).toBe(2);

    const savedResponse = await api!.get("/api/v1/settings");
    expect(savedResponse.ok()).toBe(true);
    const saved: SettingsResponse = await savedResponse.json();
    expect(saved.activity.hide_bots).toBe(nextHideBots);
    expect(saved.pull_requests.prefer_github_native_stacks).toBe(nextPreferNativeStacks);

    await page.reload();
    await openSettingsPanel(page, "Activity");
    await expect(page.getByRole("button", { name: "Toggle hide bots" })).toHaveAttribute(
      "aria-pressed",
      String(nextHideBots),
    );
    await openSettingsPanel(page, "Pull requests");
    await expect(page.getByRole("button", { name: "Prefer GitHub native stacks" })).toHaveAttribute(
      "aria-pressed",
      String(nextPreferNativeStacks),
    );
  } finally {
    releaseFirstResponse();
    await page.unrouteAll({ behavior: "ignoreErrors" });
  }
});

test("Settings saves preserve both collapse and expand overrides", async ({ page }) => {
  for (const collapsed of [true, false]) {
    const baselineResponse = await api!.get("/api/v1/settings");
    expect(baselineResponse.ok()).toBe(true);
    const baseline: SettingsResponse = await baselineResponse.json();
    const configured = await api!.put("/api/v1/settings", {
      data: { activity: { ...baseline.activity, view_mode: "threaded", collapse_threads: !collapsed } },
    });
    expect(configured.ok()).toBe(true);

    await page.goto(`${isolatedServer!.info.base_url}/`);
    await page.getByRole("button", { name: collapsed ? "Collapse all" : "Expand all", exact: true }).click();
    const caret = page.locator(".threaded-view .thread-caret").first();
    await expect(caret).toHaveAttribute("aria-expanded", String(!collapsed));

    await page.getByTitle("Settings", { exact: true }).click();
    await openSettingsPanel(page, "Activity");
    const saved = page.waitForResponse(
      (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
    );
    await page.getByRole("button", { name: "Toggle hide bots" }).click();
    expect((await saved).status()).toBe(200);
    await page.getByRole("button", { name: "Back to app" }).click();
    await expect.soft(caret).toHaveAttribute("aria-expanded", String(!collapsed));
  }
});

test("Activity search survives returning after reloading Settings", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/?view=flat&search=caching+layer`);
  const search = page.locator(".search-wrap input");
  await expect(search).toHaveValue("caching layer");
  await page.getByTitle("Settings", { exact: true }).click();
  await page.reload();
  await page.getByRole("button", { name: "Back to app" }).click();
  await expect(search).toHaveValue("caching layer");
});
