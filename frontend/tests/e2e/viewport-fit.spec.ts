import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("settings page uses the kit sidebar-and-panel layout", async ({ page }) => {
  await page.goto("/settings");

  await expect(page.locator(".settings-page")).toBeVisible();
  const nav = page.getByRole("navigation", { name: "Settings" });
  await expect(nav).toBeVisible();

  await expect(
    page.evaluate(() => {
      const main = document.querySelector<HTMLElement>(".app-main");
      const shell = document.querySelector<HTMLElement>(".kit-settings");
      const sidebar = document.querySelector<HTMLElement>(".kit-settings__sidebar");
      const content = document.querySelector<HTMLElement>(".kit-settings__content");
      if (!main || !shell || !sidebar || !content) return null;

      const mainRect = main.getBoundingClientRect();
      const shellRect = shell.getBoundingClientRect();
      const sidebarRect = sidebar.getBoundingClientRect();
      const contentRect = content.getBoundingClientRect();

      return {
        shellFillsMain:
          Math.round(shellRect.left) === Math.round(mainRect.left) &&
          Math.round(shellRect.right) === Math.round(mainRect.right),
        sidebarStartsAtMainEdge: Math.round(sidebarRect.left) === Math.round(mainRect.left),
        contentFillsRemainingWidth:
          Math.round(contentRect.left) >= Math.round(sidebarRect.right) &&
          Math.round(contentRect.right) === Math.round(mainRect.right),
        pageDoesNotScrollHorizontally: document.documentElement.scrollWidth <= window.innerWidth,
      };
    }),
  ).resolves.toEqual({
    shellFillsMain: true,
    sidebarStartsAtMainEdge: true,
    contentFillsRemainingWidth: true,
    pageDoesNotScrollHorizontally: true,
  });

  // Switched panels: selecting a category swaps the rendered section.
  await expect(page.getByRole("heading", { name: "Repositories" })).toBeVisible();
  await nav.getByRole("button", { name: "Workspace agents" }).click();
  await expect(page.getByRole("heading", { name: "Workspace agents" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Repositories" })).toBeHidden();
});

test("switching settings categories preserves unsaved drafts", async ({ page }) => {
  await page.goto("/settings");
  await expect(page.locator(".settings-page")).toBeVisible();

  const nav = page.getByRole("navigation", { name: "Settings" });
  await nav.getByRole("button", { name: "Terminal" }).click();
  const fontSize = page.getByLabel("Font size");
  await fontSize.fill("17");

  // Panels are hidden, not unmounted, on switch — an unsaved edit must
  // survive a round-trip through another category.
  await nav.getByRole("button", { name: "Activity" }).click();
  await expect(fontSize).toBeHidden();
  await nav.getByRole("button", { name: "Terminal" }).click();
  await expect(fontSize).toHaveValue("17");
});

test("settings shell stacks at kit's 760px breakpoint", async ({ page }) => {
  await page.goto("/settings");
  await expect(page.locator(".settings-page")).toBeVisible();

  const shellDirection = () =>
    page.evaluate(() => {
      const shell = document.querySelector<HTMLElement>(".kit-settings");
      return shell ? getComputedStyle(shell).flexDirection : null;
    });

  // Media-query rem resolves against the browser's 16px initial font size,
  // not the app root — kit's shared breakpoints are written in px (760px
  // here) so this boundary must be exact.
  await page.setViewportSize({ width: 760, height: 800 });
  await expect.poll(shellDirection).toBe("column");

  await page.setViewportSize({ width: 761, height: 800 });
  await expect.poll(shellDirection).toBe("row");
});

test("settings sidebar lists every panel in declaration order under group headings", async ({ page }) => {
  await page.goto("/settings");

  await expect(page.locator(".settings-page")).toBeVisible();
  await expect(
    page.evaluate(() =>
      Array.from(document.querySelectorAll<HTMLElement>(".kit-settings__nav-label, .kit-settings__group-title")).map(
        (item) => item.textContent?.trim() ?? "",
      ),
    ),
  ).resolves.toEqual([
    "Providers",
    "Repositories",
    "Workflow",
    "Pull requests",
    "Activity",
    "Workspace",
    "Terminal",
    "Kata mappings",
    "Workspace agents",
    "Fleet federation",
    "Navigation",
    "Visible modes",
  ]);
});

test("settings navigation stacks on phone-width viewports", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto("/settings");

  await expect(page.locator(".settings-page")).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(() => {
        const shell = document.querySelector<HTMLElement>(".kit-settings");
        const sidebar = document.querySelector<HTMLElement>(".kit-settings__sidebar");
        const content = document.querySelector<HTMLElement>(".kit-settings__content");
        if (!shell || !sidebar || !content) return null;

        const shellRect = shell.getBoundingClientRect();
        const sidebarRect = sidebar.getBoundingClientRect();
        const contentRect = content.getBoundingClientRect();

        return {
          navStacksAboveContent: Math.round(sidebarRect.bottom) <= Math.round(contentRect.top),
          navFitsViewport: Math.round(sidebarRect.left) >= 0 && Math.round(sidebarRect.right) <= window.innerWidth,
          contentFitsViewport: document.documentElement.scrollWidth <= window.innerWidth,
          shellFillsViewport: Math.round(shellRect.width) === window.innerWidth,
        };
      }),
    )
    .toEqual({
      navStacksAboveContent: true,
      navFitsViewport: true,
      contentFitsViewport: true,
      shellFillsViewport: true,
    });
});

test("Firefox receives compact scrollbar styling for app scroll panes", async ({ page, browserName }) => {
  test.skip(browserName !== "firefox", "Firefox-specific scrollbar regression");

  // Short viewport plus the tall Terminal panel so the settings panel
  // overflows its scroll pane.
  await page.setViewportSize({ width: 1280, height: 420 });
  await page.goto("/settings");

  await page.getByRole("navigation", { name: "Settings" }).getByRole("button", { name: "Terminal" }).click();
  const pane = page.locator(".kit-settings__scroll");
  await expect(pane).toBeVisible();
  await expect.poll(() => pane.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true);
  await expect(
    page.evaluate(() => {
      const settingsPane = document.querySelector(".kit-settings__scroll");
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
