import type { Page } from "@playwright/test";

/**
 * Settings is a switched-panel kit SettingsLayout: panels other than the
 * active category are hidden (kept mounted for drafts, but not visible or
 * clickable). Select the named category before interacting with its
 * controls — Repositories is the default on load.
 */
export async function openSettingsPanel(page: Page, label: string): Promise<void> {
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
  await page.getByRole("navigation", { name: "Settings" }).getByRole("button", { name: label }).click();
}
