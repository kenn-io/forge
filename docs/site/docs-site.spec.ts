import { expect, test } from "@playwright/test";

type HeaderStyle = {
  fontFamily: string;
  fontWeight: string;
  transform: string;
};

type FirstFrameWindow = Window & {
  __firstFrameScheme?: Promise<string | null>;
};

test("keeps the site brand stable while scrolling", async ({ page }) => {
  for (const viewport of [
    { width: 1280, height: 800 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/");

    const topics = page.locator(".md-header__topic");
    const brand = topics.first();
    const pageTitle = topics.nth(1);
    const before = await brand.evaluate<HeaderStyle>((element) => {
      const style = getComputedStyle(element);
      return {
        fontFamily: style.fontFamily,
        fontWeight: style.fontWeight,
        transform: style.transform,
      };
    });

    await page.evaluate(() => scrollTo(0, 500));
    await page.waitForTimeout(450);

    const after = await brand.evaluate<HeaderStyle>((element) => {
      const style = getComputedStyle(element);
      return {
        fontFamily: style.fontFamily,
        fontWeight: style.fontWeight,
        transform: style.transform,
      };
    });
    await expect(brand).toHaveText("kenn-forge");
    await expect(brand).toBeVisible();
    await expect(pageTitle).toBeHidden();
    expect(after.fontFamily).toBe(before.fontFamily);
    expect(after.fontWeight).toBe(before.fontWeight);
    expect(after.transform).toBe("none");
    await expect(brand).toBeInViewport();
  }
});

test("links to the canonical Forge repository and releases", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator('a[href="https://github.com/kenn-io/forge"]').first()).toBeVisible();

  await page.goto("/quickstart/");
  await expect(page.getByRole("link", { name: "GitHub Releases" })).toHaveAttribute(
    "href",
    "https://github.com/kenn-io/forge/releases",
  );
});

test("places Fleet under advanced and experimental navigation", async ({ page }) => {
  await page.goto("/");

  const primaryNav = page.locator(".md-sidebar--primary nav[data-md-level='0']");
  const advancedLabel = primaryNav.locator("label.md-nav__link", {
    hasText: "Advanced / experimental",
  });
  await expect(advancedLabel).toBeVisible();

  const advancedItem = advancedLabel.locator("xpath=ancestor::li[1]");
  await expect(advancedItem.getByRole("link", { name: "Fleet" })).toHaveAttribute(
    "href",
    /federated-fleet\/$/,
  );
  await expect(
    primaryNav.locator(":scope > ul > li > a.md-nav__link", { hasText: "Fleet" }),
  ).toHaveCount(0);
});

test("applies the browser theme before the runtime bundle", async ({ browser }) => {
  for (const preference of [
    { colorScheme: "light" as const, expected: "default" },
    { colorScheme: "dark" as const, expected: "slate" },
  ]) {
    const context = await browser.newContext({ colorScheme: preference.colorScheme });
    await context.addInitScript(() => {
      const target = window as FirstFrameWindow;
      target.__firstFrameScheme = new Promise((resolve) => {
        requestAnimationFrame(() => {
          resolve(document.body?.getAttribute("data-md-color-scheme") ?? null);
        });
      });
    });
    const page = await context.newPage();
    await page.route("**/assets/javascripts/bundle.*.min.js", (route) => route.abort());

    await page.goto("/");
    const firstFrameScheme = await page.evaluate(() =>
      (window as FirstFrameWindow).__firstFrameScheme,
    );
    expect(firstFrameScheme).toBe(preference.expected);
    await context.close();
  }
});

test("keeps following system theme changes until the reader chooses", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "slate");

  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");

  await page.reload();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");
  await context.close();
});

test("persists an explicit light override on a dark system", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "slate");

  await page.locator('label[title="Switch to light mode"]').click();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");

  await page.reload();
  await expect(page.locator("body")).toHaveAttribute("data-md-color-scheme", "default");
  await context.close();
});
