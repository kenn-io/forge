import { expect, test } from "@playwright/test";

test.setTimeout(60_000);

// The header is kit TopBar: tabs collapse into the .kit-top-bar__nav-select
// dropdown by measurement (probe row vs. space left by the side regions),
// not by a width breakpoint. These tests pick viewports comfortably on
// either side of where the full tab row fits so they assert the collapse
// behavior rather than a magic pixel value.
test.describe("container-aware layout", () => {
  test("narrow viewport shows dropdown and collapses sidebar", async ({ page }) => {
    await page.setViewportSize({ width: 400, height: 600 });
    await page.goto("/pulls?desktop=1");
    // At narrow width the sidebar is auto-collapsed, so .pull-item
    // won't be visible. Wait for the app header instead.
    await page.locator(".app-top-bar").waitFor({ state: "visible", timeout: 10_000 });

    // Narrow: dropdown navigation visible, tab group hidden.
    await expect(page.locator(".kit-top-bar__nav-select")).toBeVisible();
    await expect(page.locator(".kit-top-bar__tabs")).not.toBeAttached();

    // Sidebar should be auto-collapsed in narrow mode.
    await expect(page.locator(".kit-sidebar-layout__sidebar")).toHaveClass(/kit-sidebar-layout__sidebar--collapsed/);
  });

  test("mobile viewport wraps header controls without horizontal overflow", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 700 });
    await page.goto("/pulls?desktop=1");
    const header = page.locator(".app-top-bar");
    await header.waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".kit-top-bar__nav-select")).toBeVisible();
    await expect(page.getByRole("button", { name: "Sync" })).toBeVisible();

    const metrics = await page.evaluate(() => {
      const headerRect = document.querySelector(".app-top-bar")?.getBoundingClientRect();
      return {
        headerHeight: headerRect?.height ?? 0,
        viewportWidth: window.innerWidth,
        documentWidth: document.documentElement.scrollWidth,
        bodyWidth: document.body.scrollWidth,
      };
    });

    expect(metrics.headerHeight).toBeGreaterThanOrEqual(76);
    expect(Math.max(metrics.documentWidth, metrics.bodyWidth)).toBeLessThanOrEqual(metrics.viewportWidth);
  });

  test("medium viewport collapses page tabs and sync label", async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 768 });
    await page.goto("/pulls/github/acme/widgets/1?desktop=1");
    const header = page.locator(".app-top-bar");
    await header.waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".kit-top-bar__nav-select")).toBeVisible({
      timeout: 20_000,
    });
    await expect(page.locator(".kit-top-bar__tabs")).not.toBeAttached();
    await expect(page.getByRole("button", { name: "Sync" })).toBeVisible();
    await expect(page.locator(".sync-btn .sync-label")).not.toBeVisible();

    await page.locator(".typeahead-trigger").click();
    await expect(page.locator(".typeahead-list")).toBeVisible();
    const repoMenuStyle = await page.evaluate(() => {
      const list = document.querySelector(".typeahead-list");
      const style = list ? getComputedStyle(list) : null;
      return {
        background: style?.backgroundColor ?? "",
        borderColor: style?.borderColor ?? "",
        borderRadius: style?.borderRadius ?? "",
      };
    });

    await page.locator(".kit-top-bar__nav-select .kit-select-dropdown__trigger").click();
    const navList = page.locator(".kit-top-bar__nav-select .kit-select-dropdown__list");
    await expect(navList).toBeVisible();
    await expect(navList.getByRole("option", { name: "PRs" })).toBeVisible();
    const navMenuStyle = await page.evaluate(() => {
      const list = document.querySelector(".kit-top-bar__nav-select .kit-select-dropdown__list");
      const style = list ? getComputedStyle(list) : null;
      return {
        background: style?.backgroundColor ?? "",
        borderColor: style?.borderColor ?? "",
        borderRadius: style?.borderRadius ?? "",
      };
    });

    expect(navMenuStyle).toEqual(repoMenuStyle);

    const metrics = await page.evaluate(() => {
      const headerRect = document.querySelector(".app-top-bar")?.getBoundingClientRect();
      const syncRect = document.querySelector(".sync-btn")?.getBoundingClientRect();
      const themeRect = document.querySelector("button[title='Toggle theme']")?.getBoundingClientRect();
      const repoRect = document.querySelector(".kit-top-bar__left .typeahead")?.getBoundingClientRect();
      const navRect = document.querySelector(".kit-top-bar__nav-select")?.getBoundingClientRect();
      return {
        headerRight: headerRect?.right ?? 0,
        headerHeight: headerRect?.height ?? 0,
        navLeft: navRect?.left ?? 0,
        repoRight: repoRect?.right ?? 0,
        syncHeight: syncRect?.height ?? 0,
        syncWidth: syncRect?.width ?? 0,
        themeHeight: themeRect?.height ?? 0,
        viewportWidth: window.innerWidth,
        documentWidth: document.documentElement.scrollWidth,
        bodyWidth: document.body.scrollWidth,
      };
    });

    expect(metrics.headerRight).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.headerHeight).toBeLessThanOrEqual(52);
    // The collapsed dropdown packs next to the left region rather than
    // floating in the centered slack.
    expect(metrics.navLeft - metrics.repoRight).toBeLessThanOrEqual(20);
    expect(Math.abs(metrics.syncHeight - metrics.themeHeight)).toBeLessThanOrEqual(1);
    expect(metrics.syncWidth).toBeLessThanOrEqual(42);
    expect(Math.max(metrics.documentWidth, metrics.bodyWidth)).toBeLessThanOrEqual(metrics.viewportWidth);
  });

  test("expanded mobile sidebar fits within the viewport", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 700 });
    await page.goto("/pulls?desktop=1");
    await page.locator(".app-top-bar").waitFor({ state: "visible", timeout: 10_000 });

    await page.getByLabel("Expand sidebar").click();
    await expect(page.locator(".kit-sidebar-layout__sidebar").first()).not.toHaveClass(
      /kit-sidebar-layout__sidebar--collapsed/,
    );

    const metrics = await page.evaluate(() => {
      const sidebarRect = document.querySelector(".kit-sidebar-layout__sidebar")?.getBoundingClientRect();
      return {
        viewportWidth: window.innerWidth,
        sidebarRight: sidebarRect?.right ?? 0,
        sidebarWidth: sidebarRect?.width ?? 0,
        documentWidth: document.documentElement.scrollWidth,
        bodyWidth: document.body.scrollWidth,
      };
    });

    expect(metrics.sidebarWidth).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(metrics.sidebarRight).toBeLessThanOrEqual(metrics.viewportWidth);
    expect(Math.max(metrics.documentWidth, metrics.bodyWidth)).toBeLessThanOrEqual(metrics.viewportWidth);
  });

  test("wide viewport expands the tab row and drops the dropdown", async ({ page }) => {
    // Start narrow, then go wide to verify the measured transition.
    await page.setViewportSize({ width: 400, height: 600 });
    await page.goto("/pulls?desktop=1");
    await page.locator(".app-top-bar").waitFor({ state: "visible", timeout: 10_000 });

    await expect(page.locator(".kit-top-bar__nav-select")).toBeVisible();

    // The suite's default viewport: every other spec clicks the expanded
    // tabs here, so this width must measure as fitting.
    await page.setViewportSize({ width: 1280, height: 720 });

    await expect(page.locator(".kit-top-bar__tabs")).toBeVisible({
      timeout: 5_000,
    });
    await expect(page.locator(".kit-top-bar__nav-select")).not.toBeAttached();

    const metrics = await page.evaluate(() => {
      const syncRect = document.querySelector(".sync-btn")?.getBoundingClientRect();
      const themeRect = document.querySelector("button[title='Toggle theme']")?.getBoundingClientRect();
      return {
        syncHeight: syncRect?.height ?? 0,
        themeHeight: themeRect?.height ?? 0,
      };
    });

    expect(Math.abs(metrics.syncHeight - metrics.themeHeight)).toBeLessThanOrEqual(1);
  });
});
