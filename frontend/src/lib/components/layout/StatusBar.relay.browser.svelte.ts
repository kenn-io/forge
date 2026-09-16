import { afterEach, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import {
  mountBrowserApp,
  emitBrowserEventSource,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "../../../test/browserAppHarness.js";
import { jsonResponse } from "../../../test/mockApiFetch.js";

let mounted: MountedBrowserApp | undefined;

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  localStorage.clear();
  await resetKeyboardModuleState();
});

it("hides relay controls when the relay is off", async () => {
  await page.viewport(1280, 900);
  mounted = await mountBrowserApp("/pulls");
  await expect.element(page.getByRole("button", { name: "Show provider quota details" })).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Show relay activity" })).not.toBeInTheDocument();
});

it("opens recent relay activity and returns focus on Escape", async () => {
  await page.viewport(1280, 900);
  mounted = await mountBrowserApp("/pulls", {
    overrides: [
      (req) =>
        req.url.pathname === "/api/v1/sync/status"
          ? jsonResponse({
              running: false,
              relay: {
                last_poll_at: new Date().toISOString(),
                unavailable: false,
                recent: [
                  {
                    cursor: "stream:1",
                    repository: "team/project",
                    target: "pull_request_checks",
                    number: 7,
                    received_at: new Date().toISOString(),
                  },
                ],
              },
            })
          : null,
    ],
  });
  const trigger = page.getByRole("button", { name: "Show relay activity" });
  await trigger.click();
  const dialog = page.getByRole("dialog", { name: "Recent relay activity" });
  await expect.element(dialog).toBeVisible();
  await expect.element(dialog.getByText("team/project")).toBeVisible();
  await expect.element(dialog.getByText("PR #7 checks")).toBeVisible();
  const bounds = dialog.element().getBoundingClientRect();
  expect(bounds.bottom).toBeLessThanOrEqual(trigger.element().getBoundingClientRect().top);
  expect(bounds.left).toBeGreaterThanOrEqual(0);
  emitBrowserEventSource("sync_status", {
    running: false,
    relay: {
      last_poll_at: new Date().toISOString(),
      unavailable: false,
      recent: [
        {
          cursor: "stream:2",
          repository: "team/project",
          target: "issue",
          number: 9,
          received_at: new Date().toISOString(),
        },
      ],
    },
  });
  await expect.element(dialog.getByText("Issue #9")).toBeVisible();
  await pressKey("Escape");
  await expect.element(dialog).not.toBeInTheDocument();
  await expect.element(trigger).toHaveFocus();
});

it("keeps the control visible with an unavailable feed and no recent activity", async () => {
  await page.viewport(1280, 900);
  mounted = await mountBrowserApp("/pulls", {
    overrides: [
      (req) =>
        req.url.pathname === "/api/v1/sync/status"
          ? jsonResponse({
              running: false,
              relay: { last_poll_at: new Date().toISOString(), unavailable: true, recent: [] },
            })
          : null,
    ],
  });
  await page.getByRole("button", { name: "Show relay activity" }).click();
  const dialog = page.getByRole("dialog", { name: "Recent relay activity" });
  await expect.element(dialog.getByText("Could not reach the relay. Normal syncing continues.")).toBeVisible();
  await expect.element(dialog.getByText("No recent activity for your repositories.")).toBeVisible();
  await page.getByRole("button", { name: "Show provider quota details" }).click();
  await expect.element(dialog).not.toBeInTheDocument();
});
