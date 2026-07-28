import { expect, type Page } from "@playwright/test";

/**
 * Split the focused detail pane through the command palette.
 *
 * The per-leaf Split right / Split down icons are gone -- on a single-tab leaf they
 * were permanently disabled, and everywhere else they duplicated dragging a tab to a
 * pane edge. The palette command and the drag are what is left, and the palette is
 * the one a test can drive without synthesising a drag.
 */
export async function splitFocusedPane(page: Page, direction: "right" | "down"): Promise<void> {
  await page.keyboard.press("Meta+K");
  const search = page.getByRole("textbox", { name: "Search command palette" });
  await expect(search).toBeVisible();
  await search.fill(`split pane ${direction}`);
  await page.keyboard.press("Enter");
  await expect(search).toBeHidden();
}
