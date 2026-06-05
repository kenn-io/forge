import { expect, test } from "@playwright/test";

test.describe("detail number link copy", () => {
  test.skip(({ browserName }) => browserName !== "chromium", "Clipboard read assertions require Chromium permissions");

  test("copies the PR GitHub link from the detail number", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);

    await page.goto("/pulls/github/acme/widgets/1");
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    const numberButton = page.getByRole("button", {
      name: "Copy PR #1 link",
    });
    await expect(numberButton).toHaveText("#1");
    await numberButton.click();

    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()))
      .toBe("https://github.com/acme/widgets/pull/1");
    await expect(numberButton).toHaveAttribute("title", "Copied!");
  });

  test("copies the issue GitHub link from the detail number", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);

    await page.goto("/issues/github/acme/widgets/10");
    await page.locator(".issue-detail").waitFor({ state: "visible", timeout: 10_000 });

    const numberButton = page.getByRole("button", {
      name: "Copy issue #10 link",
    });
    await expect(numberButton).toHaveText("#10");
    await numberButton.click();

    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()))
      .toBe("https://github.com/acme/widgets/issues/10");
    await expect(numberButton).toHaveAttribute("title", "Copied!");
  });
});
