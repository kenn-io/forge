import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("Firefox receives compact scrollbar styling for app scroll panes", async ({ page, browserName }) => {
  test.skip(browserName !== "firefox", "Firefox-specific scrollbar regression");

  await page.goto("/settings");

  await expect(page.locator(".settings-page")).toBeVisible();
  await expect(page.locator(".settings-page").evaluate((pane) => pane.scrollHeight > pane.clientHeight)).resolves.toBe(
    true,
  );
  await expect(
    page.evaluate(() => {
      const settingsPane = document.querySelector(".settings-page");
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
