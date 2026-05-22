import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
} from "@playwright/test";
import {
  startIsolatedE2EServer,
  type IsolatedE2EServer,
} from "./support/e2eServer";

let isolatedServer: IsolatedE2EServer | undefined;
let api: APIRequestContext | undefined;

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

test("settings saves and reloads workspace terminal options", async ({
  page,
}) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page
    .locator(".settings-page")
    .waitFor({ state: "visible", timeout: 10_000 });

  const input = page.getByLabel("Monospace font family");
  const fontSize = page.getByLabel("Font size");
  const scrollback = page.getByLabel("Scrollback");
  const lineHeight = page.getByLabel("Line height");
  const letterSpacing = page.getByLabel("Letter spacing");
  const cursorBlink = page.getByLabel("Cursor blink");
  const renderer = page.getByLabel("Terminal renderer");
  const saveButton = page.getByRole("button", { name: "Save", exact: true });
  await expect(input).toHaveValue("");
  await expect(fontSize).toHaveValue("14");
  await expect(scrollback).toHaveValue("1000");
  await expect(lineHeight).toHaveValue("1");
  await expect(letterSpacing).toHaveValue("0");
  await expect(cursorBlink).toBeChecked();
  await expect(renderer).toHaveValue("xterm");

  await fontSize.fill("18");
  await scrollback.fill("5000");
  await lineHeight.fill("1.15");
  await letterSpacing.fill("1");
  await renderer.selectOption("ghostty-web");
  await cursorBlink.uncheck();
  await input.click();
  await input.pressSequentially('"Iosevka Term", monospace');
  await expect(saveButton).toBeEnabled();
  const saveResponsePromise = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/settings") &&
      response.request().method() === "PUT",
  );
  await saveButton.click();
  const saveResponse = await saveResponsePromise;
  const saveBody = await saveResponse.text();
  expect(
    saveResponse.status(),
    `PUT /api/v1/settings failed: ${saveBody}`,
  ).toBe(200);

  await expect
    .poll(async () => {
      if (!api) {
        throw new Error("settings terminal API context not initialized");
      }
      const response = await api.get("/api/v1/settings");
      const settings = (await response.json()) as {
        terminal: {
          font_family: string;
          font_size: number;
          scrollback: number;
          line_height: number;
          letter_spacing: number;
          cursor_blink: boolean;
          font_ligatures: boolean;
          renderer: string;
        };
      };
      return settings.terminal;
    })
    .toEqual({
      font_family: '"Iosevka Term", monospace',
      font_size: 18,
      scrollback: 5000,
      line_height: 1.15,
      letter_spacing: 1,
      cursor_blink: false,
      font_ligatures: false,
      renderer: "ghostty-web",
    });

  await page.reload();
  await page
    .locator(".settings-page")
    .waitFor({ state: "visible", timeout: 10_000 });
  await expect(page.getByLabel("Monospace font family")).toHaveValue(
    '"Iosevka Term", monospace',
  );
  await expect(page.getByLabel("Font size")).toHaveValue("18");
  await expect(page.getByLabel("Scrollback")).toHaveValue("5000");
  await expect(page.getByLabel("Line height")).toHaveValue("1.15");
  await expect(page.getByLabel("Letter spacing")).toHaveValue("1");
  await expect(page.getByLabel("Cursor blink")).not.toBeChecked();
  await expect(page.getByLabel("Terminal renderer")).toHaveValue("ghostty-web");
});
