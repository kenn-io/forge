import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test.describe("migrated global shortcuts", () => {
  test("j and k navigate the PR list", async ({ page }) => {
    await page.goto("/pulls");
    await page.waitForSelector("[data-test='pr-list']");
    await page.keyboard.press("j");
    await expect(page.locator(".pr-list-row.selected").first()).toBeVisible();
    await page.keyboard.press("k");
    await expect(page.locator(".pr-list-row.selected").first()).toBeVisible();
  });

  test("Cmd+[ toggles the sidebar", async ({ page }) => {
    await page.goto("/pulls");
    const sidebar = page.locator("[data-test='sidebar']");
    const wasCollapsed = (await sidebar.getAttribute("data-collapsed")) === "true";
    await page.keyboard.press("Meta+BracketLeft");
    await expect(sidebar).toHaveAttribute("data-collapsed", (!wasCollapsed).toString());
  });

  test("Cmd+[ is reserved on Activity without toggling the sidebar", async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem("middleman-sidebar", "collapsed");
    });
    await page.goto("/");

    const expandButton = page.getByRole("button", {
      name: "Expand sidebar",
    });
    await expect(expandButton).toBeVisible();
    await expect(page.locator("header kbd[aria-label$='-[']")).toHaveCount(0);
    await page.evaluate(() => {
      const state = window as Window & {
        __middleman_last_bracket_default_prevented?: boolean | null;
      };
      state.__middleman_last_bracket_default_prevented = null;
      window.addEventListener("keydown", (event) => {
        if ((event.metaKey || event.ctrlKey) && event.key === "[") {
          state.__middleman_last_bracket_default_prevented = event.defaultPrevented;
        }
      });
    });

    await page.keyboard.press("Meta+BracketLeft");

    await expect
      .poll(() =>
        page.evaluate(
          () =>
            (
              window as Window & {
                __middleman_last_bracket_default_prevented?: boolean | null;
              }
            ).__middleman_last_bracket_default_prevented,
        ),
      )
      .toBe(true);
    await expect(expandButton).toBeVisible();
  });
});
