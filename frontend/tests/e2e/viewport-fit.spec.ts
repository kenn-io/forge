import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("settings page uses a sidebar and scrollable content pane", async ({ page }) => {
  await page.goto("/settings");

  await expect(page.locator(".settings-page")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible();
  await expect(page.getByPlaceholder("Search settings...")).toBeVisible();
  await expect(
    page.evaluate(() => {
      const main = document.querySelector<HTMLElement>(".app-main");
      const shell = document.querySelector<HTMLElement>(".settings-shell");
      const sidebar = document.querySelector<HTMLElement>(".settings-sidebar");
      const pane = document.querySelector<HTMLElement>(".settings-scroll-pane");
      const content = document.querySelector<HTMLElement>(".settings-page");
      if (!main || !shell || !sidebar || !pane || !content) return null;

      const mainRect = main.getBoundingClientRect();
      const shellRect = shell.getBoundingClientRect();
      const sidebarRect = sidebar.getBoundingClientRect();
      const paneRect = pane.getBoundingClientRect();
      const contentRect = content.getBoundingClientRect();
      const beforeTop = contentRect.top;
      pane.scrollTop = 120;
      const afterTop = content.getBoundingClientRect().top;

      return {
        shellFillsMain:
          Math.round(shellRect.left) === Math.round(mainRect.left) &&
          Math.round(shellRect.right) === Math.round(mainRect.right),
        sidebarStartsAtMainEdge: Math.round(sidebarRect.left) === Math.round(mainRect.left),
        paneFillsRemainingWidth:
          Math.round(paneRect.left) >= Math.round(sidebarRect.right) &&
          Math.round(paneRect.right) === Math.round(mainRect.right),
        contentIsCentered:
          Math.abs(contentRect.left + contentRect.width / 2 - (paneRect.left + paneRect.width / 2)) < 1,
        paneCanScroll: pane.scrollHeight > pane.clientHeight,
        contentMovesWithPane: afterTop < beforeTop,
      };
    }),
  ).resolves.toEqual({
    shellFillsMain: true,
    sidebarStartsAtMainEdge: true,
    paneFillsRemainingWidth: true,
    contentIsCentered: true,
    paneCanScroll: true,
    contentMovesWithPane: true,
  });

  await page
    .getByRole("navigation", { name: "Settings sections" })
    .getByRole("button", { name: "Workspace agents" })
    .click();
  await expect(page.locator("#settings-agents")).toBeInViewport();
});

test("settings sidebar order matches rendered section order", async ({ page }) => {
  await page.goto("/settings");

  await expect(page.locator(".settings-page")).toBeVisible();
  await expect(
    page.evaluate(() => {
      const navLabel = (label: string) =>
        label.replace("Activity feed defaults", "Activity").replace("Workspace terminal", "Terminal");
      const navOrder = Array.from(document.querySelectorAll<HTMLElement>(".settings-nav-item span")).map(
        (item) => item.textContent?.trim() ?? "",
      );
      const sectionOrder = Array.from(document.querySelectorAll<HTMLElement>(".settings-section > h2")).map((section) =>
        navLabel(section.textContent?.trim() ?? ""),
      );

      return { navOrder, sectionOrder, orderMatches: navOrder.join("\n") === sectionOrder.join("\n") };
    }),
  ).resolves.toEqual({
    navOrder: ["Repositories", "Activity", "Terminal", "Workspace agents", "Fleet federation", "Visible modes"],
    sectionOrder: ["Repositories", "Activity", "Terminal", "Workspace agents", "Fleet federation", "Visible modes"],
    orderMatches: true,
  });
});

test("settings navigation stacks below Tailwind lg breakpoint", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto("/settings");

  await expect(page.locator(".settings-page")).toBeVisible();
  await expect(
    page.evaluate(() => {
      const shell = document.querySelector<HTMLElement>(".settings-shell");
      const sidebar = document.querySelector<HTMLElement>(".settings-sidebar");
      const pane = document.querySelector<HTMLElement>(".settings-scroll-pane");
      if (!shell || !sidebar || !pane) return null;

      const shellRect = shell.getBoundingClientRect();
      const sidebarRect = sidebar.getBoundingClientRect();
      const paneRect = pane.getBoundingClientRect();

      return {
        navStacksAboveContent: Math.round(sidebarRect.bottom) <= Math.round(paneRect.top),
        navFitsViewport: Math.round(sidebarRect.left) >= 0 && Math.round(sidebarRect.right) <= window.innerWidth,
        contentFitsViewport: document.documentElement.scrollWidth <= window.innerWidth,
        shellFillsViewport: Math.round(shellRect.width) === window.innerWidth,
      };
    }),
  ).resolves.toEqual({
    navStacksAboveContent: true,
    navFitsViewport: true,
    contentFitsViewport: true,
    shellFillsViewport: true,
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
