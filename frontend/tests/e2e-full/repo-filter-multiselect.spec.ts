// The multi-select, owner-cascade, and flattened-single-repo-owner cases
// moved to component-level Vitest coverage in
// frontend/src/lib/components/RepoTypeahead.multiselect.test.ts. Only the
// case below remains here: it depends on the browser's native
// mousedown -> focus default action (jsdom does not perform default
// actions), so it cannot be honestly asserted outside a real browser.
import { expect, test, type Page } from "@playwright/test";

async function waitForIssueList(page: Page): Promise<void> {
  await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

test("keyboard navigation survives a real checkbox click", async ({ page }) => {
  // A real click (not just mousedown) on a row checkbox must not steal focus
  // from the filter input. The checkbox is a focusable native input and its
  // mousedown stops propagation (skipping the list's preventBlur), so without
  // preventDefault the click would blur the input and kill keyboard handling,
  // which is bound only to that input.
  await page.goto("/issues");
  await waitForIssueList(page);

  const selector = page.getByTitle("Select repository");
  await selector.click();

  const input = page.getByPlaceholder("Filter repos...");
  await expect(input).toBeFocused();

  // Real click on a leaf repo's checkbox.
  await page
    .getByRole("option", {
      name: "github.com/acme/widgets",
      exact: true,
    })
    .locator("input[type='checkbox']")
    .click();
  await expect(
    page
      .getByRole("option", {
        name: "github.com/acme/widgets",
        exact: true,
      })
      .locator("input[type='checkbox']"),
  ).toBeChecked();

  // Focus must still be on the input, and keyboard handling must still work:
  // Escape closes the dropdown.
  await expect(input).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.locator(".typeahead-list")).toHaveCount(0);
});
