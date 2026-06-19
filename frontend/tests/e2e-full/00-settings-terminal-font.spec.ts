// The 00- filename prefix schedules this long-running spec first:
// Playwright dispatches files in path order, and multi-second tests
// that start near the end of the run stretch the suite tail.

import { execFileSync } from "node:child_process";
import { expect, request as playwrightRequest, test, type APIRequestContext, type Page } from "@playwright/test";
import { startIsolatedE2EServer, startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

let isolatedServer: IsolatedE2EServer | undefined;
let api: APIRequestContext | undefined;

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  error_message?: string | null;
};

type ModeVisibility = Record<string, boolean>;

const lockedWorkspaceTestTimeoutMs = 120_000;

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

async function waitForWorkspaceReady(
  context: APIRequestContext,
  workspaceId: string,
): Promise<WorkspaceStatusResponse> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await context.get(`/api/v1/workspaces/${workspaceId}`);
    expect(response.ok()).toBe(true);
    const workspace = (await response.json()) as WorkspaceStatusResponse;
    if (workspace.status === "ready") {
      return workspace;
    }
    if (workspace.status === "error") {
      throw new Error(workspace.error_message ?? `workspace ${workspaceId} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  throw new Error(`workspace ${workspaceId} did not become ready`);
}

async function terminalScreenSizeKey(page: Page): Promise<string> {
  return await page.locator(".terminal-container .xterm-screen").evaluate((element) => {
    const style = getComputedStyle(element);
    return `${style.width}x${style.height}`;
  });
}

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

test("settings saves and reloads workspace terminal options", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  const input = page.getByLabel("Monospace font family");
  const fontSize = page.getByLabel("Font size");
  const scrollback = page.getByLabel("Scrollback");
  const lineHeight = page.getByLabel("Line height");
  const letterSpacing = page.getByLabel("Letter spacing");
  const cursorBlink = page.getByLabel("Cursor blink");
  const renderer = page.getByRole("combobox", {
    name: "Terminal renderer: xterm.js",
  });
  const saveButton = page.getByRole("button", {
    name: "Save",
    exact: true,
  });
  await expect(input).toHaveValue("");
  await expect(fontSize).toHaveValue("14");
  await expect(scrollback).toHaveValue("1000");
  await expect(lineHeight).toHaveValue("1");
  await expect(letterSpacing).toHaveValue("0");
  await expect(cursorBlink).toBeChecked();
  await expect(page.locator("select#terminal-renderer")).toHaveCount(0);
  await expect(renderer).toHaveText("xterm.js");

  await fontSize.fill("18");
  await scrollback.fill("5000");
  await lineHeight.fill("1.15");
  await letterSpacing.fill("1");
  await renderer.click();
  await page.getByRole("option", { name: "ghostty-web" }).click();
  await cursorBlink.uncheck();
  await input.click();
  await input.pressSequentially('"Iosevka Term", monospace');
  await expect(saveButton).toBeEnabled();
  const saveResponsePromise = page.waitForResponse(
    (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
  );
  await saveButton.click();
  const saveResponse = await saveResponsePromise;
  const saveBody = await saveResponse.text();
  expect(saveResponse.status(), `PUT /api/v1/settings failed: ${saveBody}`).toBe(200);

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
          hide_tmux_status: boolean;
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
      hide_tmux_status: false,
      renderer: "ghostty-web",
    });

  await page.reload();
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
  await expect(page.getByLabel("Monospace font family")).toHaveValue('"Iosevka Term", monospace');
  await expect(page.getByLabel("Font size")).toHaveValue("18");
  await expect(page.getByLabel("Scrollback")).toHaveValue("5000");
  await expect(page.getByLabel("Line height")).toHaveValue("1.15");
  await expect(page.getByLabel("Letter spacing")).toHaveValue("1");
  await expect(page.getByLabel("Cursor blink")).not.toBeChecked();
  await expect(
    page.getByRole("combobox", {
      name: "Terminal renderer: ghostty-web",
    }),
  ).toHaveText("ghostty-web");
});

test("settings saves visible modes and hides disabled nav entries", async ({ page }) => {
  if (!api) throw new Error("settings API context not initialized");
  const originalResponse = await api.get("/api/v1/settings");
  const originalSettings = (await originalResponse.json()) as { modes: ModeVisibility };
  const originalModes = { ...originalSettings.modes };

  try {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(`${isolatedServer!.info.base_url}/settings`);
    await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.getByLabel("PRs")).toBeChecked();

    await page.getByLabel("PRs").uncheck();
    const saveResponsePromise = page.waitForResponse(
      (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
    );
    await page.getByRole("button", { name: "Save visible modes" }).click();
    const saveResponse = await saveResponsePromise;
    const saveBody = await saveResponse.text();
    expect(saveResponse.status(), `PUT /api/v1/settings failed: ${saveBody}`).toBe(200);

    await expect
      .poll(async () => {
        const response = await api!.get("/api/v1/settings");
        const settings = (await response.json()) as { modes: ModeVisibility };
        return settings.modes.pulls;
      })
      .toBe(false);

    await page.goto(`${isolatedServer!.info.base_url}/`);
    await expect(page.getByRole("link", { name: "PRs" })).toHaveCount(0);
    await page.reload();
    await expect(page.getByRole("link", { name: "PRs" })).toHaveCount(0);

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${isolatedServer!.info.base_url}/m`);
    await expect(page.locator(".mobile-tabs").getByText("PRs", { exact: true })).toHaveCount(0);
  } finally {
    const restore = await api.put("/api/v1/settings", { data: { modes: originalModes } });
    expect(restore.ok()).toBe(true);
  }
});

test.describe("terminal options popover", () => {
  test.describe.configure({ timeout: lockedWorkspaceTestTimeoutMs });

  test("live previews, reverts unsaved changes, and saves from the toolbar", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let workspaceServer: IsolatedE2EServer | null = null;
    let workspaceApi: APIRequestContext | null = null;
    try {
      workspaceServer = await startIsolatedWorkspaceE2EServer();
      workspaceApi = await playwrightRequest.newContext({
        baseURL: workspaceServer.info.base_url,
      });

      const createResponse = await workspaceApi.post("/api/v1/issues/github/acme/widgets/10/workspace", {
        data: {},
      });
      expect(createResponse.status()).toBe(202);
      const workspace = (await createResponse.json()) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(workspaceApi, workspace.id);

      await page.goto(`${workspaceServer.info.base_url}/terminal/${workspace.id}`);
      await page.getByRole("button", { name: "Open terminal panel" }).click();
      await expect(page.locator(".terminal-container .xterm-screen")).toBeVisible();
      await expect.poll(() => terminalScreenSizeKey(page)).not.toBe("0pxx0px");
      const initialScreenSize = await terminalScreenSizeKey(page);

      await page.getByRole("button", { name: "Terminal options" }).click();
      const terminalOptionsDialog = page.getByRole("dialog", { name: "Terminal options" });
      await expect(terminalOptionsDialog).toBeVisible();
      await expect(terminalOptionsDialog.getByText("Visible modes")).toHaveCount(0);
      await expect(terminalOptionsDialog.getByRole("button", { name: "Save visible modes" })).toHaveCount(0);
      await page.getByLabel("Font size").fill("20");
      await expect.poll(() => terminalScreenSizeKey(page)).not.toBe(initialScreenSize);

      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog", { name: "Terminal options" })).toBeHidden();
      await expect.poll(() => terminalScreenSizeKey(page)).toBe(initialScreenSize);

      await page.getByRole("button", { name: "Terminal options" }).click();
      await page.getByLabel("Font size").fill("18");
      await expect.poll(() => terminalScreenSizeKey(page)).not.toBe(initialScreenSize);
      let releaseSettingsSave: (() => void) | undefined;
      await page.route("**/api/v1/settings", async (route) => {
        if (route.request().method() === "PUT" && releaseSettingsSave === undefined) {
          await new Promise<void>((resolve) => {
            releaseSettingsSave = resolve;
          });
        }
        await route.continue();
      });
      const saveResponsePromise = page.waitForResponse(
        (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
      );
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await expect.poll(() => releaseSettingsSave !== undefined).toBe(true);
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog", { name: "Terminal options" })).toBeVisible();
      releaseSettingsSave?.();
      const saveResponse = await saveResponsePromise;
      const saveBody = await saveResponse.text();
      expect(saveResponse.status(), `PUT /api/v1/settings failed: ${saveBody}`).toBe(200);

      await expect
        .poll(async () => {
          const response = await workspaceApi!.get("/api/v1/settings");
          const settings = (await response.json()) as {
            terminal: { font_size: number };
          };
          return settings.terminal.font_size;
        })
        .toBe(18);

      await page.keyboard.press("Escape");
      await expect.poll(() => terminalScreenSizeKey(page)).not.toBe(initialScreenSize);
    } finally {
      await workspaceApi?.dispose();
      await workspaceServer?.stop();
    }
  });
});
