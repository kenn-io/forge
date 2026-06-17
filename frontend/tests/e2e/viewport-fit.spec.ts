import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("settings page scrolls from the full-width main pane", async ({ page }) => {
  await page.goto("/settings");

  await expect(page.locator(".settings-page")).toBeVisible();
  await expect(
    page.evaluate(() => {
      const main = document.querySelector<HTMLElement>(".app-main");
      const pane = document.querySelector<HTMLElement>(".settings-scroll-pane");
      const content = document.querySelector<HTMLElement>(".settings-page");
      if (!main || !pane || !content) return null;

      const mainRect = main.getBoundingClientRect();
      const paneRect = pane.getBoundingClientRect();
      const contentRect = content.getBoundingClientRect();
      const beforeTop = contentRect.top;
      pane.scrollTop = 120;
      const afterTop = content.getBoundingClientRect().top;

      return {
        paneFillsMain:
          Math.round(paneRect.left) === Math.round(mainRect.left) &&
          Math.round(paneRect.right) === Math.round(mainRect.right),
        contentIsCentered:
          Math.abs(contentRect.left + contentRect.width / 2 - (mainRect.left + mainRect.width / 2)) < 1,
        paneCanScroll: pane.scrollHeight > pane.clientHeight,
        contentMovesWithPane: afterTop < beforeTop,
      };
    }),
  ).resolves.toEqual({
    paneFillsMain: true,
    contentIsCentered: true,
    paneCanScroll: true,
    contentMovesWithPane: true,
  });
});

test("Firefox receives compact scrollbar styling for app scroll panes", async ({ page, browserName }) => {
  test.skip(browserName !== "firefox", "Firefox-specific scrollbar regression");

  await page.goto("/settings");

  await expect(page.locator(".settings-scroll-pane")).toBeVisible();
  await expect(
    page.locator(".settings-scroll-pane").evaluate((pane) => pane.scrollHeight > pane.clientHeight),
  ).resolves.toBe(true);
  await expect(
    page.evaluate(() => {
      const settingsPane = document.querySelector(".settings-scroll-pane");
      const appRules = Array.from(document.styleSheets)
        .flatMap((sheet) => {
          try {
            return Array.from(sheet.cssRules);
          } catch {
            return [];
          }
        })
        .filter((rule): rule is CSSStyleRule => "selectorText" in rule);

      return appRules.some(
        (rule) =>
          settingsPane?.matches(rule.selectorText) === true &&
          rule.style.scrollbarWidth === "thin" &&
          rule.style.scrollbarColor.includes("transparent"),
      );
    }),
  ).resolves.toBe(true);

  await expect(
    page.evaluate(() => {
      const appRect = document.querySelector("#app")?.getBoundingClientRect();

      return {
        heightFits: appRect?.height === window.innerHeight,
        widthFits: appRect?.width === window.innerWidth,
      };
    }),
  ).resolves.toEqual({ heightFits: true, widthFits: true });
});
