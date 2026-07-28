import { expect, type Page } from "@playwright/test";

/** Run a palette command by its exact label without depending on the current focus owner. */
export async function runPaletteCommand(page: Page, label: string): Promise<void> {
  await page.getByRole("button", { name: "Open command palette" }).click();
  const dialog = page.getByRole("dialog", { name: "Command palette" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("textbox", { name: "Search command palette" }).fill(label);
  const row = dialog.locator("button.palette-row", { hasText: label }).first();
  await expect(row).toBeVisible();
  await row.click();
  await expect(dialog).toBeHidden();
}
