import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test.describe("palette focus trap", () => {
  test("Tab and Shift+Tab cycle within the open palette", async ({ page }) => {
    await page.goto("/pulls");
    // The palette is opened from anywhere via Meta+K (Ctrl+K on
    // non-mac). Both bindings are wired to palette.open in
    // defaultActions; Playwright's Meta key chord matches the mac
    // convention used elsewhere in the suite.
    await page.keyboard.press("Meta+K");

    const dialog = page.getByRole("dialog", {
      name: "Command palette",
    });
    await expect(dialog).toBeVisible();

    const input = page.locator(".palette-input");
    await expect(input).toBeFocused();

    // Forward Tab: focus must stay inside the .palette dialog. The
    // current palette markup has a single focusable element (the
    // input), so the trap keeps focus there; once additional
    // focusable rows land it should still cycle without escaping.
    await page.keyboard.press("Tab");
    const afterTab = await page.evaluate(() => {
      const palette = document.querySelector(".palette");
      return !!palette && palette.contains(document.activeElement);
    });
    expect(afterTab).toBe(true);

    // Reverse Tab: same containment guarantee.
    await page.keyboard.press("Shift+Tab");
    const afterShiftTab = await page.evaluate(() => {
      const palette = document.querySelector(".palette");
      return !!palette && palette.contains(document.activeElement);
    });
    expect(afterShiftTab).toBe(true);

    // Escape closes the palette and tears down the modal frame.
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
  });
});
