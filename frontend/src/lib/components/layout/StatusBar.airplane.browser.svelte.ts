import { afterEach, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "../../../test/browserAppHarness.js";
import { jsonResponse, mockSettings } from "../../../test/mockApiFetch.js";

let mounted: MountedBrowserApp | undefined;

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  localStorage.clear();
  await resetKeyboardModuleState();
});

it("opens the airplane-mode setting from its status indicator and clears it when disabled", async () => {
  await page.viewport(1280, 900);
  let airplaneMode = true;
  mounted = await mountBrowserApp("/pulls", {
    overrides: [
      (req) => {
        if (req.url.pathname !== "/api/v1/settings") return null;
        if (req.method === "PUT") {
          airplaneMode = JSON.parse(req.bodyText).airplane_mode;
        }
        return jsonResponse({ ...mockSettings, airplane_mode: airplaneMode });
      },
    ],
  });

  const indicator = page.getByRole("button", { name: "Airplane mode: open Sync settings" });
  await expect.element(indicator).toBeVisible();
  await indicator.click();
  const toggle = page.getByRole("switch", { name: "Airplane mode" });
  await expect.element(toggle).toBeVisible();
  await expect.element(toggle).toBeChecked();

  await page
    .getByRole("navigation", { name: "Settings" })
    .getByRole("button", { name: /^Repositories/ })
    .click();
  await expect.element(page.getByRole("heading", { name: "Repositories", exact: true })).toBeVisible();
  await indicator.click();
  await expect.element(toggle).toBeVisible();
  await toggle.click();
  await expect.element(toggle).not.toBeChecked();
  await expect.element(indicator).not.toBeInTheDocument();
});
