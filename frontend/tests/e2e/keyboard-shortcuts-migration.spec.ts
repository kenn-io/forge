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

    await page.locator("main.app-main").click();
    await page.keyboard.press("Meta+K");
    const palette = page.getByRole("dialog", { name: "Command palette" });
    await expect(palette).toBeVisible();
    await expect(palette.getByText("Toggle sidebar", { exact: true })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(palette).toBeHidden();

    await page.locator("main.app-main").click();
    await page.keyboard.press("Shift+/");
    const cheatsheet = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(cheatsheet).toBeVisible();
    await expect(cheatsheet.getByText("Toggle sidebar", { exact: true })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(cheatsheet).toBeHidden();

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

  test("Cmd+[ is reserved on compact PR list without toggling persisted sidebar state", async ({ page }) => {
    await page.setViewportSize({ width: 480, height: 800 });
    await page.goto("/pulls");

    await expect(page.locator(".sidebar")).toHaveCount(0);
    await page.evaluate(() => {
      localStorage.removeItem("middleman-sidebar");
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
    await expect.poll(() => page.evaluate(() => localStorage.getItem("middleman-sidebar"))).toBeNull();
  });
});
